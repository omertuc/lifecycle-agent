package seedclusterinfo

import (
	"github.com/openshift-kni/lifecycle-agent/utils"
)

// SeedClusterInfo is a struct that contains information about the seed cluster
// that was used to create the seed image. It is meant to be serialized to a
// file on the seed image. It has multiple purposes, see the documentation of
// each field for more information.
//
// Changes to this struct should not be made lightly, as it will break
// backwards compatibilitiy with existing seed images. If you've made a
// breaking change, you will need to increment the [SeedFormatVersion] constant
// to avoid silently breakage and allow for backwards compatibility code.
type SeedClusterInfo struct {
	// The OCP version of the seed cluster that was used to create this seed
	// image. During an IBU, lifecycle-agent will compare the user's desired
	// version with the seed cluster's version to ensure the image the user is
	// using is actually using the version they expect. During an IBI, this
	// parameter is ignored.
	SeedClusterOCPVersion string `json:"seed_cluster_ocp_version,omitempty"`

	// The base domain of the seed cluster that was used to create this seed
	// image. This in combination with the cluster name is used to construct
	// the cluster's full domain name. That domain name is required when we ask
	// recert to replace the domain in certificates, as recert needs both the
	// original domain (the seed's) and the new domain to perform the replace.
	BaseDomain string `json:"base_domain,omitempty"`

	// See BaseDomain documentation above.
	ClusterName string `json:"cluster_name,omitempty"`

	// The IP of the seed cluster's SNO node. This is used when we sed the IP
	// address of the seed to replace it with the desired IP address of the
	// cluster.
	NodeIP string `json:"node_ip,omitempty"`

	// The container registry used to host the release image of the seed cluster.
	// TODO: Document what this is for
	// TODO: Is this really necessary? Find a way to get rid of this
	ReleaseRegistry string `json:"release_registry,omitempty"`

	// Whether or not the seed cluster was configured to use a mirror registry or not.
	// TODO: Document what this is for
	// TODO: Is this really necessary? Find a way to get rid of this
	MirrorRegistryConfigured bool `json:"mirror_registry_configured,omitempty"`

	// The hostname of the seed cluster's SNO node. This hostname is required
	// when we ask recert to replace the original hostname in certificates, as
	// recert needs both the original hostname and the new hostname to perform
	// the replace.
	SNOHostname string `json:"sno_hostname,omitempty"`

	// The recert image pull-spec that was used by the seed cluster. This is
	// used to run recert using the same version of recert that was used to
	// create the seed image (the seed cluster runs recert to expire the
	// certificates, so it has already proven to run successfully on the seed
	// data).
	RecertImagePullSpec string `json:"recert_image_pull_spec,omitempty"`
}

func NewFromClusterInfo(clusterInfo *utils.ClusterInfo, seedImagePullSpec string) *SeedClusterInfo {
	return &SeedClusterInfo{
		SeedClusterOCPVersion:    clusterInfo.OCPVersion,
		BaseDomain:               clusterInfo.BaseDomain,
		ClusterName:              clusterInfo.ClusterName,
		NodeIP:                   clusterInfo.NodeIP,
		ReleaseRegistry:          clusterInfo.ReleaseRegistry,
		SNOHostname:              clusterInfo.Hostname,
		MirrorRegistryConfigured: clusterInfo.MirrorRegistryConfigured,
		RecertImagePullSpec:      seedImagePullSpec,
	}
}

func ReadSeedClusterInfoFromFile(path string) (*SeedClusterInfo, error) {
	data := &SeedClusterInfo{}
	err := utils.ReadYamlOrJSONFile(path, data)
	return data, err
}
