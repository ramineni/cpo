package test

import (
	"fmt"
        "os/exec"
        "strings"
	"time"

        "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/storage/testpatterns"
	"k8s.io/kubernetes/test/e2e/storage/testsuites"
)

type cinderDriver struct {
	driverInfo testsuites.DriverInfo
	manifests  []string
}

var Cinderdriver = InitCinderDriver

type cinderVolume struct {
	ID               string
	Name             string
	Status           string
	AvailabilityZone string
	f                *framework.Framework
}

// initCinderDriver returns cinderDriver that implements TestDriver interface
func initCinderDriver(name string, manifests ...string) testsuites.TestDriver {
	return &cinderDriver{
		driverInfo: testsuites.DriverInfo{
			Name:        name,
			MaxFileSize: testpatterns.FileSizeLarge,
			SupportedFsType: sets.NewString(
				"", // Default fsType
				"ext2",
				"ext3",
				"ext4",
				"xfs",
			),
			Capabilities: map[testsuites.Capability]bool{
				testsuites.CapPersistence: true,
				testsuites.CapFsGroup:     true,
				testsuites.CapExec:        true,
				testsuites.CapMultiPODs:   true,
			},
		},
		manifests: manifests,
	}
}

func InitCinderDriver() testsuites.TestDriver {

	return initCinderDriver("cinder.csi.openstack.org",
		"cinder-csi-controllerplugin.yaml",
		"cinder-csi-controllerplugin-rbac.yaml",
		"cinder-csi-nodeplugin.yaml",
		"cinder-csi-nodeplugin-rbac.yaml",
		"csi-secret-cinderplugin.yaml")

}

var _ testsuites.TestDriver = &cinderDriver{}

var _ testsuites.PreprovisionedVolumeTestDriver = &cinderDriver{}
var _ testsuites.PreprovisionedPVTestDriver = &cinderDriver{}
var _ testsuites.DynamicPVTestDriver = &cinderDriver{}

func (d *cinderDriver) GetDriverInfo() *testsuites.DriverInfo {
	return &d.driverInfo
}

func (d *cinderDriver) SkipUnsupportedTest(pattern testpatterns.TestPattern) {
}

func (d *cinderDriver) GetPersistentVolumeSource(readOnly bool, fsType string, volume testsuites.TestVolume) (*v1.PersistentVolumeSource, *v1.VolumeNodeAffinity) {
	vol, _ := volume.(*cinderVolume)
	return &v1.PersistentVolumeSource{
		CSI: &v1.CSIPersistentVolumeSource{
			Driver:       d.driverInfo.Name,
			VolumeHandle: vol.ID,
			VolumeAttributes: map[string]string{
				"ID":               vol.ID,
				"Name":             vol.Name,
				"AvailabilityZone": vol.AvailabilityZone,
				"status":           vol.Status,
			},
		},
	}, nil
}

func (c *cinderDriver) CreateVolume(config *testsuites.PerTestConfig, volType testpatterns.TestVolType) testsuites.TestVolume {
	f := config.Framework
	ns := f.Namespace

	// We assume that namespace.Name is a random string
	volumeName := ns.Name
	//ginkgo.By("creating a test Cinder volume")
	output, err := exec.Command("cinder", "create", "--display-name="+volumeName, "1").CombinedOutput()
	outputString := string(output[:])
	framework.Logf("cinder output:\n%s", outputString)
	framework.ExpectNoError(err)

	// Parse 'id'' from stdout. Expected format:
	// |     attachments     |                  []                  |
	// |  availability_zone  |                 nova                 |
	// ...
	// |          id         | 1d6ff08f-5d1c-41a4-ad72-4ef872cae685 |
	volumeID := ""
	for _, line := range strings.Split(outputString, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 5 {
			continue
		}
		if fields[1] != "id" {
			continue
		}
		volumeID = fields[3]
		break
	}
	framework.Logf("Volume ID: %s", volumeID)
	framework.ExpectNotEqual(volumeID, "")
	return &cinderVolume{
		Name: volumeName,
		ID:   volumeID,
	}
}

func (v *cinderVolume) DeleteVolume() {
	name := v.Name

	// Try to delete the volume for several seconds - it takes
	// a while for the plugin to detach it.
	var output []byte
	var err error
	timeout := time.Second * 120

	framework.Logf("Waiting up to %v for removal of cinder volume %s", timeout, name)
	for start := time.Now(); time.Since(start) < timeout; time.Sleep(5 * time.Second) {
		output, err = exec.Command("cinder", "delete", name).CombinedOutput()
		if err == nil {
			framework.Logf("Cinder volume %s deleted", name)
			return
		}
		framework.Logf("Failed to delete volume %s: %v", name, err)
	}
	framework.Logf("Giving up deleting volume %s: %v\n%s", name, err, string(output[:]))
}

func (d *cinderDriver) GetDynamicProvisionStorageClass(config *testsuites.PerTestConfig, fsType string) *storagev1.StorageClass {
	provisioner := "cinder.csi.openstack.org"
	parameters := map[string]string{}
	if fsType != "" {
		parameters["fsType"] = fsType
	}
	ns := config.Framework.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", d.driverInfo.Name)

	return testsuites.GetStorageClass(provisioner, parameters, nil, ns, suffix)
}

func (d *cinderDriver) GetClaimSize() string {
	return "2Gi"
}

func (d *cinderDriver) PrepareTest(f *framework.Framework) (*testsuites.PerTestConfig, func()) {
	config := &testsuites.PerTestConfig{
		Driver:    d,
		Prefix:    "cinder",
		Framework: f,
	}

	return config, func() {}
}
