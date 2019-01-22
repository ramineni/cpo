package openstack

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
        "k8s.io/klog"
)

const (
	defaultMetadataVersion = "2012-08-10"
	metadataURLTemplate    = "http://169.254.169.254/openstack/%s/meta_data.json"
)

type metadata struct {
	UUID             string
	AvailabilityZone string "json:\"availability_zone\""
}

func getMetadata(metadataURL string) ([]byte, error) {
	klog.V(4).Infof("anu: Getmetadata called")
	resp, err := http.Get(metadataURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, err
	}

	md, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	klog.V(4).Infof("anu: Getmetadata value %v", md)
	return md, nil
}

// GetMetaDataInfo retrieves from metadata service and returns
// info in metadata struct
func getMetaDataInfo() (metadata, error) {
	klog.V(4).Infof("anu: Getmetadata info called")
	metadataURL := fmt.Sprintf(metadataURLTemplate, defaultMetadataVersion)
	var m metadata
	md, err := getMetadata(metadataURL)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(md, &m)
	if err != nil {
		return m, err
	}
	klog.V(4).Infof("anu: Getmetadata info value %v", m)
	return m, nil
}

// GetInstanceID from metadata service
func GetInstanceID() (string, error) {
	klog.V(4).Infof("anu: GetInstance id info called")
	md, err := getMetaDataInfo()
	if err != nil {
		return "", err
	}
	klog.V(4).Infof("anu: GetInstance id info called is %s %s", md.UUID, md.AvailabilityZone)
	return md.UUID, nil
}

// GetAvailabilityZone from metadata service
func GetAvailabilityZone() (string, error) {
	klog.V(4).Infof("anu: GetAvailability zzone info called")
	md, err := getMetaDataInfo()
	if err != nil {
		return "", err
	}
	klog.V(4).Infof("anu: GetInstance id info called is %s %s", md.UUID, md.AvailabilityZone)
	return md.AvailabilityZone, nil
}
