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

package openstack

import (
	"fmt"
	"io/ioutil"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/volumeattach"
	"k8s.io/apimachinery/pkg/util/wait"
	cpoerrors "k8s.io/cloud-provider-openstack/pkg/util/errors"
	metadataUtil "k8s.io/cloud-provider-openstack/pkg/util/metadata"

	"k8s.io/klog"
)

const (
	VolumeAvailableStatus    = "available"
	VolumeInUseStatus        = "in-use"
	VolumeDeletedStatus      = "deleted"
	VolumeErrorStatus        = "error"
	operationFinishInitDelay = 1 * time.Second
	operationFinishFactor    = 1.1
	operationFinishSteps     = 10
	diskAttachInitDelay      = 1 * time.Second
	diskAttachFactor         = 1.2
	diskAttachSteps          = 15
	diskDetachInitDelay      = 1 * time.Second
	diskDetachFactor         = 1.2
	diskDetachSteps          = 13
	volumeDescription        = "Created by OpenStack Cinder CSI driver"
)

type Volume struct {
	// ID of the instance, to which this volume is attached. "" if not attached
	AttachedServerId string
	// Device file path
	AttachedDevice string
	// Unique identifier for the volume.
	ID string
	// Human-readable display name for the volume.
	Name string
	// Current status of the volume.
	Status string
	// Volume size in GB
	Size int
	// Availability Zone the volume belongs to
	AZ string
}

// CreateVolume creates a volume of given size
func (os *OpenStack) CreateVolume(name string, size int, vtype, availability string, snapshotID string, tags *map[string]string) (string, string, int, error) {
	opts := &volumes.CreateOpts{
		Name:             name,
		Size:             size,
		VolumeType:       vtype,
		AvailabilityZone: availability,
		Description:      volumeDescription,
		SnapshotID:       snapshotID,
	}
	if tags != nil {
		opts.Metadata = *tags
	}

	vol, err := volumes.Create(os.blockstorage, opts).Extract()
	if err != nil {
		return "", "", 0, err
	}

	return vol.ID, vol.AvailabilityZone, vol.Size, nil
}

// ListVolumes list all the volumes
func (os *OpenStack) ListVolumes() ([]Volume, error) {

	var vlist []Volume
	opts := volumes.ListOpts{}
	pages, err := volumes.List(os.blockstorage, opts).AllPages()
	if err != nil {
		return vlist, err
	}
	vols, err := volumes.ExtractVolumes(pages)
	if err != nil {
		return vlist, err
	}

	for _, v := range vols {
		volume := Volume{
			ID:     v.ID,
			Name:   v.Name,
			Status: v.Status,
			AZ:     v.AvailabilityZone,
			Size:   v.Size,
		}
		vlist = append(vlist, volume)
	}
	return vlist, nil
}

// GetVolumesByName is a wrapper around ListVolumes that creates a Name filter to act as a GetByName
// Returns a list of Volume references with the specified name
func (os *OpenStack) GetVolumesByName(n string) ([]Volume, error) {
	var vlist []Volume
	opts := volumes.ListOpts{Name: n}
	pages, err := volumes.List(os.blockstorage, opts).AllPages()
	if err != nil {
		return vlist, err
	}

	vols, err := volumes.ExtractVolumes(pages)
	if err != nil {
		return vlist, err
	}

	for _, v := range vols {
		volume := Volume{
			ID:     v.ID,
			Name:   v.Name,
			Status: v.Status,
			Size:   v.Size,
			AZ:     v.AvailabilityZone,
		}
		vlist = append(vlist, volume)
	}
	return vlist, nil
}

// DeleteVolume delete a volume
func (os *OpenStack) DeleteVolume(volumeID string) error {
	used, err := os.diskIsUsed(volumeID)
	if err != nil {
		return err
	}
	if used {
		return fmt.Errorf("Cannot delete the volume %q, it's still attached to a node", volumeID)
	}

	err = volumes.Delete(os.blockstorage, volumeID, nil).ExtractErr()
	return err
}

// GetVolume retrieves Volume by its ID.
func (os *OpenStack) GetVolume(volumeID string) (Volume, error) {

	vol, err := volumes.Get(os.blockstorage, volumeID).Extract()
	if err != nil {
		return Volume{}, err
	}

	volume := Volume{
		ID:     vol.ID,
		Name:   vol.Name,
		Status: vol.Status,
	}

	if len(vol.Attachments) > 0 {
		volume.AttachedServerId = vol.Attachments[0].ServerID
		volume.AttachedDevice = vol.Attachments[0].Device
	}

	return volume, nil
}

// AttachVolume attaches given cinder volume to the compute
func (os *OpenStack) AttachVolume(instanceID, volumeID string) (string, error) {
	volume, err := os.GetVolume(volumeID)
	if err != nil {
		return "", err
	}

	if volume.AttachedServerId != "" {
		if instanceID == volume.AttachedServerId {
			klog.V(4).Infof("Disk %s is already attached to instance %s", volumeID, instanceID)
			return volume.ID, nil
		}
		return "", fmt.Errorf("disk %s is attached to a different instance (%s)", volumeID, volume.AttachedServerId)
	}

	_, err = volumeattach.Create(os.compute, instanceID, &volumeattach.CreateOpts{
		VolumeID: volume.ID,
	}).Extract()

	if err != nil {
		return "", fmt.Errorf("failed to attach %s volume to %s compute: %v", volumeID, instanceID, err)
	}
	klog.V(2).Infof("Successfully attached %s volume to %s compute", volumeID, instanceID)
	return volume.ID, nil
}

// WaitDiskAttached waits for attched
func (os *OpenStack) WaitDiskAttached(instanceID string, volumeID string) error {
	backoff := wait.Backoff{
		Duration: diskAttachInitDelay,
		Factor:   diskAttachFactor,
		Steps:    diskAttachSteps,
	}

	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		attached, err := os.diskIsAttached(instanceID, volumeID)
		if err != nil && !cpoerrors.IsNotFound(err) {
			// if this is a race condition indicate the volume is deleted
			// during sleep phase, ignore the error and return attach=false
			return false, err
		}
		return attached, nil
	})

	if err == wait.ErrWaitTimeout {
		err = fmt.Errorf("Volume %q failed to be attached within the alloted time", volumeID)
	}

	return err
}

// DetachVolume detaches given cinder volume from the compute
func (os *OpenStack) DetachVolume(instanceID, volumeID string) error {
	volume, err := os.GetVolume(volumeID)
	if err != nil {
		return err
	}
	if volume.Status == VolumeAvailableStatus {
		klog.V(2).Infof("volume: %s has been detached from compute: %s ", volume.ID, instanceID)
		return nil
	}

	if volume.Status != VolumeInUseStatus {
		return fmt.Errorf("can not detach volume %s, its status is %s", volume.Name, volume.Status)
	}

	if volume.AttachedServerId != instanceID {
		return fmt.Errorf("disk: %s has no attachments or is not attached to compute: %s", volume.Name, instanceID)
	} else {
		err = volumeattach.Delete(os.compute, instanceID, volume.ID).ExtractErr()
		if err != nil {
			return fmt.Errorf("failed to delete volume %s from compute %s attached %v", volume.ID, instanceID, err)
		}
		klog.V(2).Infof("Successfully detached volume: %s from compute: %s", volume.ID, instanceID)
	}

	return nil
}

// WaitDiskDetached waits for detached
func (os *OpenStack) WaitDiskDetached(instanceID string, volumeID string) error {
	backoff := wait.Backoff{
		Duration: diskDetachInitDelay,
		Factor:   diskDetachFactor,
		Steps:    diskDetachSteps,
	}

	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		attached, err := os.diskIsAttached(instanceID, volumeID)
		if err != nil {
			return false, err
		}
		return !attached, nil
	})

	if err == wait.ErrWaitTimeout {
		err = fmt.Errorf("Volume %q failed to detach within the alloted time", volumeID)
	}

	return err
}

// GetAttachmentDiskPath gets device path of attached volume to the compute
func (os *OpenStack) GetAttachmentDiskPath(instanceID, volumeID string) (string, error) {
	volume, err := os.GetVolume(volumeID)
	if err != nil {
		return "", err
	}
	if volume.Status != VolumeInUseStatus {
		return "", fmt.Errorf("can not get device path of volume %s, its status is %s ", volume.Name, volume.Status)
	}
	if volume.AttachedServerId != "" {
		if instanceID == volume.AttachedServerId {
			return volume.AttachedDevice, nil
		} else {
			return "", fmt.Errorf("disk %q is attached to a different compute: %q, should be detached before proceeding", volumeID, volume.AttachedServerId)
		}
	}
	return "", fmt.Errorf("volume %s has no ServerId", volumeID)
}

// GetDevicePath returns the path of an attached block storage volume, specified by its id.
func (os *OpenStack) GetDevicePath(volumeID string) (string, error) {
	backoff := wait.Backoff{
		Duration: operationFinishInitDelay,
		Factor:   operationFinishFactor,
		Steps:    operationFinishSteps,
	}

	var devicePath string
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		devicePath = os.getDevicePathBySerialID(volumeID)
		if devicePath != "" {
			return true, nil
		}
		devicePath = os.getDevicePathFromInstanceMetadata(volumeID)
		if devicePath != "" {
			return true, nil
		}
		return false, nil
	})

	if err == wait.ErrWaitTimeout {
		return "", fmt.Errorf("Failed to find device for the volumeID: %q within the alloted time", volumeID)
	} else if devicePath == "" {
		return "", fmt.Errorf("Device path was empty for volumeID: %q", volumeID)
	}
	return devicePath, nil
}

// GetDevicePathBySerialID returns the path of an attached block storage volume, specified by its id.
func (os *OpenStack) getDevicePathBySerialID(volumeID string) string {
	// Build a list of candidate device paths.
	// Certain Nova drivers will set the disk serial ID, including the Cinder volume id.
	candidateDeviceNodes := []string{
		// KVM
		fmt.Sprintf("virtio-%s", volumeID[:20]),
		// KVM virtio-scsi
		fmt.Sprintf("scsi-0QEMU_QEMU_HARDDISK_%s", volumeID[:20]),
		// ESXi
		fmt.Sprintf("wwn-0x%s", strings.Replace(volumeID, "-", "", -1)),
	}

	files, err := ioutil.ReadDir("/dev/disk/by-id/")
	if err != nil {
		klog.V(4).Infof("ReadDir failed with error %v", err)
	}

	for _, f := range files {
		for _, c := range candidateDeviceNodes {
			if c == f.Name() {
				klog.V(4).Infof("Found disk attached as %q; full devicepath: %s\n",
					f.Name(), path.Join("/dev/disk/by-id/", f.Name()))
				return path.Join("/dev/disk/by-id/", f.Name())
			}
		}
	}

	klog.V(4).Infof("Failed to find device for the volumeID: %q by serial ID", volumeID)
	return ""
}

func (os *OpenStack) getDevicePathFromInstanceMetadata(volumeID string) string {
	// Nova Hyper-V hosts cannot override disk SCSI IDs. In order to locate
	// volumes, we're querying the metadata service. Note that the Hyper-V
	// driver will include device metadata for untagged volumes as well.
	//
	// We're avoiding using cached metadata (or the configdrive),
	// relying on the metadata service.
	instanceMetadata, err := metadataUtil.GetFromMetadataService("latest")

	if err != nil {
		klog.V(4).Infof("Could not retrieve instance metadata. Error: %v", err)
		return ""
	}

	for _, device := range instanceMetadata.Devices {
		if device.Type == "disk" && device.Serial == volumeID {
			klog.V(4).Infof(
				"Found disk metadata for volumeID %q. Bus: %q, Address: %q",
				volumeID, device.Bus, device.Address)

			diskPattern := fmt.Sprintf("/dev/disk/by-path/*-%s-%s", device.Bus, device.Address)
			diskPaths, err := filepath.Glob(diskPattern)
			if err != nil {
				klog.Errorf(
					"could not retrieve disk path for volumeID: %q. Error filepath.Glob(%q): %v",
					volumeID, diskPattern, err)
				return ""
			}

			if len(diskPaths) == 1 {
				return diskPaths[0]
			}

			klog.Errorf(
				"expecting to find one disk path for volumeID %q, found %d: %v",
				volumeID, len(diskPaths), diskPaths)
			return ""
		}
	}

	klog.V(4).Infof("Could not retrieve device metadata for volumeID: %q", volumeID)
	return ""
}

// diskIsAttached queries if a volume is attached to a compute instance
func (os *OpenStack) diskIsAttached(instanceID, volumeID string) (bool, error) {
	volume, err := os.GetVolume(volumeID)
	if err != nil {
		return false, err
	}

	return instanceID == volume.AttachedServerId, nil
}

// diskIsUsed returns true a disk is attached to any node.
func (os *OpenStack) diskIsUsed(volumeID string) (bool, error) {
	volume, err := os.GetVolume(volumeID)
	if err != nil {
		return false, err
	}
	return volume.AttachedServerId != "", nil
}
