package main

import (
	"flag"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	_ "k8s.io/cloud-provider-openstack/test"
	"k8s.io/kubernetes/test/e2e/framework"
)

func init() {
	framework.HandleFlags()
	framework.AfterReadingAllFlags(&framework.TestContext)
}

func Test(t *testing.T) {
	flag.Parse()
	RegisterFailHandler(Fail)
	RunSpecs(t, "CSI Suite")
}

func main() {
	Test(&testing.T{})
}
