module github.com/gardener/machine-controller-manager

go 1.12

require (
	cloud.google.com/go v0.45.1 // indirect
	contrib.go.opencensus.io/exporter/ocagent v0.2.0 // indirect
	github.com/Azure/azure-sdk-for-go v26.1.0+incompatible
	github.com/Azure/go-ansiterm v0.0.0-20170929234023-d6e3b3328b78 // indirect
	github.com/Azure/go-autorest v11.5.0+incompatible
	github.com/aliyun/alibaba-cloud-sdk-go v0.0.0-20190329064014-6e358769c32a
	github.com/aws/aws-sdk-go v1.22.0
	github.com/census-instrumentation/opencensus-proto v0.1.0 // indirect
	github.com/davecgh/go-spew v1.1.1
	github.com/docker/docker v0.7.3-0.20180612054059-a9fbbdc8dd87 // indirect
	github.com/emicklei/go-restful v2.9.6+incompatible // indirect
	github.com/go-openapi/spec v0.19.2
	github.com/gogo/protobuf v1.2.1 // indirect
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/google/btree v1.0.0 // indirect
	github.com/google/gofuzz v0.0.0-20170612174753-24818f796faf // indirect
	github.com/google/uuid v1.1.1 // indirect
	github.com/googleapis/gnostic v0.2.0 // indirect
	github.com/gophercloud/gophercloud v0.0.0-20190212181753-892256c46858
	github.com/gophercloud/utils v0.0.0-20190527093828-25f1b77b8c03
	github.com/gotestyourself/gotestyourself v2.2.0+incompatible // indirect
	github.com/gregjones/httpcache v0.0.0-20181110185634-c63ab54fda8f // indirect
	github.com/imdario/mergo v0.3.4 // indirect
	github.com/json-iterator/go v1.1.6 // indirect
	github.com/mitchellh/copystructure v1.0.0
	github.com/mitchellh/mapstructure v1.1.2
	github.com/onsi/ginkgo v1.8.0
	github.com/onsi/gomega v1.5.0
	github.com/packethost/packngo v0.0.0-20181217122008-b3b45f1b4979
	github.com/peterbourgon/diskv v2.0.1+incompatible // indirect
	github.com/pkg/errors v0.8.1 // indirect
	github.com/prometheus/client_golang v0.9.3-0.20190127221311-3c4408c8b829
	github.com/smartystreets/goconvey v0.0.0-20190330032615-68dc04aab96a // indirect
	github.com/spf13/pflag v1.0.3
	github.com/terraform-providers/terraform-provider-vsphere v1.12.0
	github.com/vmware/govmomi v0.21.0
	golang.org/x/crypto v0.0.0-20190701094942-4def268fd1a4 // indirect
	golang.org/x/lint v0.0.0-20190409202823-959b441ac422
	golang.org/x/oauth2 v0.0.0-20190604053449-0f29369cfe45
	golang.org/x/sys v0.0.0-20190804053845-51ab0e2deafa // indirect
	google.golang.org/api v0.9.0
	gotest.tools v2.2.0+incompatible // indirect
	k8s.io/api v0.0.0-20181005203742-357ec6384fa7
	k8s.io/apimachinery v0.0.0-20180913025736-6dd46049f395
	k8s.io/apiserver v0.0.0-20181005205051-9f398e330d7f
	k8s.io/client-go v0.0.0-20181005204318-cb4883f3dea0
	k8s.io/code-generator v0.0.0-20180823001027-3dcf91f64f63
	k8s.io/gengo v0.0.0-20190327210449-e17681d19d3a // indirect
	k8s.io/klog v0.3.3 // indirect
	k8s.io/kube-openapi v0.0.0-20180216212618-50ae88d24ede
)

replace (
	github.com/Sirupsen/logrus => github.com/sirupsen/logrus v1.2.0
	github.com/onsi/ginkgo => github.com/onsi/ginkgo v1.8.0
	github.com/onsi/gomega => github.com/onsi/gomega v1.5.0
	k8s.io/api => k8s.io/api v0.0.0-20181005203742-357ec6384fa7 // kubernetes-1.12.1
	k8s.io/apimachinery => k8s.io/apimachinery v0.0.0-20180913025736-6dd46049f395 // kubernetes-1.12.1
	k8s.io/apiserver => k8s.io/apiserver v0.0.0-20181005205051-9f398e330d7f // kubernetes-1.12.1
	k8s.io/client-go => k8s.io/client-go v0.0.0-20181005204318-cb4883f3dea0 // kubernetes-1.12.1
	k8s.io/code-generator => k8s.io/code-generator v0.0.0-20180823001027-3dcf91f64f63 // kubernetes-1.12.1
	k8s.io/kube-openapi => k8s.io/kube-openapi v0.0.0-20180216212618-50ae88d24ede
)
