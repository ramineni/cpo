/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cinder

import (
	"github.com/container-storage-interface/spec/lib/go/csi/v0"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog"

	"k8s.io/cloud-provider-openstack/pkg/csi/cinder/mount"
	"k8s.io/cloud-provider-openstack/pkg/csi/cinder/openstack"
)

type nodeServer struct {
	Driver *CinderDriver
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	klog.V(4).Infof("NodePublishVolume: called with args %+v", *req)

	source := req.GetStagingTargetPath()
	targetPath := req.GetTargetPath()
	readOnly := req.GetReadonly()
	fsType := req.GetVolumeCapability().GetMount().GetFsType()

	// Get Mount Provider
	m, err := mount.GetMountProvider()
	if err != nil {
		klog.V(3).Infof("Failed to GetMountProvider: %v", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	mountOptions := []string{"bind"}
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	} else {
		mountOptions = append(mountOptions, "rw")
	}

	// Verify whether mounted
	notMnt, err := m.IsLikelyNotMountPointAttach(targetPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Volume Mount
	if notMnt {
		// Perform a bind mount
		options := []string{"bind"}
		if readOnly {
			options = append(options, "ro")
		} else {
			options = append(options, "rw")
		}
		// Mount
		err = m.Mount(source, targetPath, fsType, options)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {

	targetPath := req.GetTargetPath()

	// Get Mount Provider
	m, err := mount.GetMountProvider()
	if err != nil {
		klog.V(3).Infof("Failed to GetMountProvider: %v", err)
		return nil, err
	}

	notMnt, err := m.IsLikelyNotMountPointDetach(targetPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if notMnt {
		return nil, status.Error(codes.NotFound, "Volume not mounted")
	}

	err = m.UnmountPath(targetPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	stagingTargetPath := req.GetStagingTargetPath()

	// Get Mount Provider
	m, err := mount.GetMountProvider()
	if err != nil {
		klog.V(3).Infof("Failed to GetMountProvider: %v", err)
		return nil, err
	}

	notMnt, err := m.IsLikelyNotMountPointDetach(stagingTargetPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if notMnt {
		return nil, status.Error(codes.NotFound, "Volume not mounted")
	}

	err = m.UnmountPath(stagingTargetPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	klog.V(4).Infof("NodeStageVolume: called with args %+v", *req)

	stagingTarget := req.GetStagingTargetPath()
	volumeCapability := req.GetVolumeCapability()
	if len(stagingTarget) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Staging target not provided")
	}
	devicePath, ok := req.GetPublishInfo()["DevicePath"]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "Device path not provided")
	}
	// Get Mount Provider
	m, err := mount.GetMountProvider()
	if err != nil {
		klog.V(3).Infof("Failed to GetMountProvider: %v", err)
		return nil, status.Errorf(codes.Internal, "Failed to GetMountProvider: %v", err)
	}
	// Device Scan
	err = m.ScanForAttach(devicePath)
	if err != nil {
		klog.V(3).Infof("Failed to ScanForAttach: %v", err)
		return nil, status.Errorf(codes.Internal, "Failed to ScanForAttach: %v", err)
	}

	// Verify whether mounted
	notMnt, err := m.IsLikelyNotMountPointAttach(stagingTarget)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Volume Mount
	if notMnt {
		// Default fstype is ext4
		fsType := "ext4"
		var options []string
		if mnt := volumeCapability.GetMount(); mnt != nil {
			fsType = volumeCapability.GetMount().GetFsType()
			mountFlags := volumeCapability.GetMount().GetMountFlags()
			options = append(options, mountFlags...)
		} else if blk := volumeCapability.GetBlock(); blk != nil {
			// TODO(#341): Block volume support
			return nil, status.Errorf(codes.Unimplemented, "Block volume support is not yet implemented")
		}
		// Mount
		err = m.FormatAndMount(devicePath, stagingTarget, fsType, options)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	return &csi.NodeStageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeGetId(ctx context.Context, req *csi.NodeGetIdRequest) (*csi.NodeGetIdResponse, error) {

	nodeID, err := getNodeID()
	if err != nil {
		return nil, err
	}

	return &csi.NodeGetIdResponse{
		NodeId: nodeID,
	}, nil
}

func (ns *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {

	nodeID, err := getNodeID()
	if err != nil {
		return nil, err
	}

	return &csi.NodeGetInfoResponse{
		NodeId: nodeID,
	}, nil
}

func (ns *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	klog.V(5).Infof("Using default NodeGetCapabilities")

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_UNKNOWN,
					},
				},
			},
		},
	}, nil
}

func getNodeIDMountProvider() (string, error) {

	// Get Mount Provider
	m, err := mount.GetMountProvider()
	if err != nil {
		klog.V(3).Infof("Failed to GetMountProvider: %v", err)
		return "", err
	}

	nodeID, err := m.GetInstanceID()
	if err != nil {
		klog.V(3).Infof("Failed to GetInstanceID: %v", err)
		return "", err
	}

	return nodeID, nil
}

func getNodeIDMetdataService() (string, error) {
	nodeID, err := openstack.GetInstanceID()
	if err != nil {
		return "", err
	}
	return nodeID, nil
}

func getNodeID() (string, error) {
	// First try to get instance id from mount provider
	nodeID, err := getNodeIDMountProvider()
	if err == nil || nodeID != "" {
		return nodeID, nil
	}

	klog.V(3).Infof("Failed to GetInstanceID from mount data: %v", err)
	klog.V(3).Info("Trying to GetInstanceID from metadata service")
	nodeID, err = getNodeIDMetdataService()
	if err != nil {
		klog.V(3).Infof("Failed to GetInstanceID from metadata service: %v", err)
		return "", err
	}
	return nodeID, nil
}
