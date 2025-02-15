package clusterconfig

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	v1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift-kni/lifecycle-agent/api/seedreconfig"
	"github.com/openshift-kni/lifecycle-agent/internal/common"
	"github.com/openshift-kni/lifecycle-agent/utils"
)

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.openshift.io,resources=imagedigestmirrorsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=operator.openshift.io,resources=imagecontentsourcepolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.openshift.io,resources=proxies,verbs=get;list;watch
// +kubebuilder:rbac:groups=machineconfiguration.openshift.io,resources=machineconfigs,verbs=get;list;watch

const (
	manifestDir = "manifests"

	proxyName     = "cluster"
	proxyFileName = "proxy.json"

	pullSecretName = "pull-secret"

	idmsFileName  = "image-digest-mirror-set.json"
	icspsFileName = "image-content-source-policy-list.json"

	caBundleCMName   = "user-ca-bundle"
	caBundleFileName = caBundleCMName + ".json"

	// ssh authorized keys file created by mco from ssh machine configs
	sshKeyFile = "/home/core/.ssh/authorized_keys.d/ignition"
)

var (
	hostPath                = common.Host
	listOfNetworkFilesPaths = []string{
		common.NMConnectionFolder,
	}
)

type UpgradeClusterConfigGatherer interface {
	FetchClusterConfig(ctx context.Context, ostreeVarDir string) error
	FetchLvmConfig(ctx context.Context, ostreeVarDir string) error
}

// UpgradeClusterConfigGather Gather ClusterConfig attributes from the kube-api
type UpgradeClusterConfigGather struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// FetchClusterConfig collects the current cluster's configuration and write it as JSON files into
// given filesystem directory.
func (r *UpgradeClusterConfigGather) FetchClusterConfig(ctx context.Context, ostreeVarDir string) error {
	r.Log.Info("Fetching cluster configuration")

	clusterConfigPath, err := r.configDir(ostreeVarDir)
	if err != nil {
		return err
	}
	manifestsDir := filepath.Join(clusterConfigPath, manifestDir)

	if err := r.fetchProxy(ctx, manifestsDir); err != nil {
		return err
	}
	if err := r.fetchIDMS(ctx, manifestsDir); err != nil {
		return err
	}

	if err := r.fetchClusterInfo(ctx, clusterConfigPath); err != nil {
		return err
	}
	if err := r.fetchCABundle(ctx, manifestsDir, clusterConfigPath); err != nil {
		return err
	}
	if err := r.fetchICSPs(ctx, manifestsDir); err != nil {
		return err
	}
	if err := r.fetchNetworkConfig(ostreeVarDir); err != nil {
		return err
	}

	r.Log.Info("Successfully fetched cluster configuration")
	return nil
}

func (r *UpgradeClusterConfigGather) fetchPullSecret(ctx context.Context) (string, error) {
	r.Log.Info("Fetching pull-secret")
	return utils.GetSecretData(
		ctx, common.PullSecretName, common.OpenshiftConfigNamespace, corev1.DockerConfigJsonKey, r.Client)
}

func (r *UpgradeClusterConfigGather) fetchProxy(ctx context.Context, manifestsDir string) error {
	r.Log.Info("Fetching cluster-wide proxy", "name", proxyName)

	proxy := v1.Proxy{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: proxyName}, &proxy); err != nil {
		return err
	}

	p := v1.Proxy{
		ObjectMeta: metav1.ObjectMeta{
			Name: proxy.Name,
		},
		Spec: proxy.Spec,
	}
	typeMeta, err := r.typeMetaForObject(&p)
	if err != nil {
		return err
	}
	p.TypeMeta = *typeMeta

	filePath := filepath.Join(manifestsDir, proxyFileName)
	r.Log.Info("Writing proxy to file", "path", filePath)
	return utils.MarshalToFile(p, filePath)
}

func (r *UpgradeClusterConfigGather) fetchSSHPublicKey() (string, error) {
	sshKey, err := os.ReadFile(filepath.Join(hostPath, sshKeyFile))
	if err != nil {
		return "", err
	}
	return string(sshKey), err
}

func (r *UpgradeClusterConfigGather) fetchInfraID(ctx context.Context) (string, error) {
	infra, err := utils.GetInfrastructure(ctx, r.Client)
	if err != nil {
		return "", fmt.Errorf("failed to get infrastructure: %w", err)
	}

	return infra.Status.InfrastructureName, nil
}

func (r *UpgradeClusterConfigGather) GetKubeadminPasswordHash(ctx context.Context) (string, error) {
	kubeadminPasswordHash, err := utils.GetSecretData(ctx, "kubeadmin", "kube-system", "kubeadmin", r.Client)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return "", fmt.Errorf("failed to get kubeadmin password hash: %w", err)
		}

		// No kubeadmin password secret found, this is fine (see
		// https://docs.openshift.com/container-platform/4.14/authentication/remove-kubeadmin.html)
		//
		// An empty string will signal to the seed LCA that it should delete
		// the kubeadmin password secret of the seed cluster, to ensure we
		// don't accept a password in the reconfigured seed.
		return "", nil

	}

	return kubeadminPasswordHash, nil
}

func SeedReconfigurationFromClusterInfo(clusterInfo *utils.ClusterInfo,
	kubeconfigCryptoRetention *seedreconfig.KubeConfigCryptoRetention, sshKey, infraID, pullSecret, kubeadminPasswordHash string) *seedreconfig.SeedReconfiguration {
	return &seedreconfig.SeedReconfiguration{
		APIVersion:                seedreconfig.SeedReconfigurationVersion,
		BaseDomain:                clusterInfo.BaseDomain,
		ClusterName:               clusterInfo.ClusterName,
		ClusterID:                 clusterInfo.ClusterID,
		InfraID:                   infraID,
		NodeIP:                    clusterInfo.NodeIP,
		ReleaseRegistry:           clusterInfo.ReleaseRegistry,
		Hostname:                  clusterInfo.Hostname,
		KubeconfigCryptoRetention: *kubeconfigCryptoRetention,
		SSHKey:                    sshKey,
		PullSecret:                pullSecret,
		KubeadminPasswordHash:     kubeadminPasswordHash,
	}
}

func (r *UpgradeClusterConfigGather) fetchClusterInfo(ctx context.Context, clusterConfigPath string) error {
	r.Log.Info("Fetching ClusterInfo")

	clusterInfo, err := utils.GetClusterInfo(ctx, r.Client)
	if err != nil {
		return err
	}

	seedReconfigurationKubeconfigRetention, err := utils.SeedReconfigurationKubeconfigRetentionFromCluster(ctx, r.Client)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig retention from crypto dir: %w", err)
	}

	sshKey, err := r.fetchSSHPublicKey()
	if err != nil {
		return err
	}

	infraID, err := r.fetchInfraID(ctx)
	if err != nil {
		return err
	}

	pullSecret, err := r.fetchPullSecret(ctx)
	if err != nil {
		return err
	}

	kubeadminPasswordHash, err := r.GetKubeadminPasswordHash(ctx)
	if err != nil {
		return err
	}

	seedReconfiguration := SeedReconfigurationFromClusterInfo(clusterInfo, seedReconfigurationKubeconfigRetention,
		sshKey,
		infraID,
		pullSecret,
		kubeadminPasswordHash,
	)

	filePath := filepath.Join(clusterConfigPath, common.SeedReconfigurationFileName)
	r.Log.Info("Writing ClusterInfo to file", "path", filePath)
	return utils.MarshalToFile(seedReconfiguration, filePath)
}

func (r *UpgradeClusterConfigGather) fetchIDMS(ctx context.Context, manifestsDir string) error {
	r.Log.Info("Fetching IDMS")
	idms, err := r.getIDMSs(ctx)
	if err != nil {
		return err
	}

	if len(idms.Items) < 1 {
		r.Log.Info("ImageDigestMirrorSetList is empty, skipping")
		return nil
	}

	filePath := filepath.Join(manifestsDir, idmsFileName)
	r.Log.Info("Writing IDMS to file", "path", filePath)
	return utils.MarshalToFile(idms, filePath)
}

// configDirs creates and returns the directory for the given cluster configuration.
func (r *UpgradeClusterConfigGather) configDir(ostreeVarDir string) (string, error) {
	filesDir := filepath.Join(ostreeVarDir, common.OptOpenshift, common.ClusterConfigDir)
	r.Log.Info("Creating cluster configuration folder and subfolder", "folder", filesDir)
	if err := os.MkdirAll(filepath.Join(filesDir, manifestDir), 0o700); err != nil {
		return "", err
	}
	return filesDir, nil
}

// typeMetaForObject returns the given object's TypeMeta or an error otherwise.
func (r *UpgradeClusterConfigGather) typeMetaForObject(o runtime.Object) (*metav1.TypeMeta, error) {
	gvks, unversioned, err := r.Scheme.ObjectKinds(o)
	if err != nil {
		return nil, err
	}
	if unversioned || len(gvks) == 0 {
		return nil, fmt.Errorf("unable to find API version for object")
	}
	// if there are multiple assume the last is the most recent
	gvk := gvks[len(gvks)-1]
	return &metav1.TypeMeta{
		APIVersion: gvk.GroupVersion().String(),
		Kind:       gvk.Kind,
	}, nil
}

func (r *UpgradeClusterConfigGather) cleanObjectMetadata(o client.Object) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      o.GetName(),
		Namespace: o.GetNamespace(),
		Labels:    o.GetLabels(),
	}
}

func (r *UpgradeClusterConfigGather) getIDMSs(ctx context.Context) (v1.ImageDigestMirrorSetList, error) {
	idmsList := v1.ImageDigestMirrorSetList{}
	currentIdms := v1.ImageDigestMirrorSetList{}
	if err := r.Client.List(ctx, &currentIdms); err != nil {
		return v1.ImageDigestMirrorSetList{}, err
	}

	for _, idms := range currentIdms.Items {
		obj := v1.ImageDigestMirrorSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      idms.Name,
				Namespace: idms.Namespace,
			},
			Spec: idms.Spec,
		}
		typeMeta, err := r.typeMetaForObject(&currentIdms)
		if err != nil {
			return v1.ImageDigestMirrorSetList{}, err
		}
		idms.TypeMeta = *typeMeta

		idmsList.Items = append(idmsList.Items, obj)
	}
	typeMeta, err := r.typeMetaForObject(&idmsList)
	if err != nil {
		return v1.ImageDigestMirrorSetList{}, err
	}
	idmsList.TypeMeta = *typeMeta

	return idmsList, nil
}

func (r *UpgradeClusterConfigGather) fetchICSPs(ctx context.Context, manifestsDir string) error {
	r.Log.Info("Fetching ICSPs")
	iscpsList := &operatorv1alpha1.ImageContentSourcePolicyList{}
	currentIcps := &operatorv1alpha1.ImageContentSourcePolicyList{}
	if err := r.Client.List(ctx, currentIcps); err != nil {
		return err
	}

	if len(currentIcps.Items) < 1 {
		r.Log.Info("ImageContentPolicyList is empty, skipping")
		return nil
	}

	for _, icp := range currentIcps.Items {
		obj := operatorv1alpha1.ImageContentSourcePolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      icp.Name,
				Namespace: icp.Namespace,
			},
			Spec: icp.Spec,
		}
		typeMeta, err := r.typeMetaForObject(&icp)
		if err != nil {
			return err
		}
		icp.TypeMeta = *typeMeta
		iscpsList.Items = append(iscpsList.Items, obj)
	}
	typeMeta, err := r.typeMetaForObject(iscpsList)
	if err != nil {
		return err
	}
	iscpsList.TypeMeta = *typeMeta

	if err := utils.MarshalToFile(iscpsList, filepath.Join(manifestsDir, icspsFileName)); err != nil {
		return fmt.Errorf("failed to write icsps to %s, err: %w",
			filepath.Join(manifestsDir, icspsFileName), err)
	}

	return nil
}

func (r *UpgradeClusterConfigGather) fetchCABundle(ctx context.Context, manifestsDir, clusterConfigPath string) error {
	r.Log.Info("Fetching user ca bundle")
	caBundle := &corev1.ConfigMap{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: caBundleCMName,
		Namespace: common.OpenshiftConfigNamespace}, caBundle)
	if err != nil && errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get ca bundle cm, err: %w", err)
	}

	typeMeta, err := r.typeMetaForObject(caBundle)
	if err != nil {
		return err
	}
	caBundle.TypeMeta = *typeMeta
	caBundle.ObjectMeta = r.cleanObjectMetadata(caBundle)

	if err := utils.MarshalToFile(caBundle, filepath.Join(manifestsDir, caBundleFileName)); err != nil {
		return fmt.Errorf("failed to write user ca bundle to %s, err: %w",
			filepath.Join(manifestsDir, caBundleFileName), err)
	}

	// we should copy ca-bundle from snoa as without doing it we will fail to pull images
	// workaround for https://issues.redhat.com/browse/OCPBUGS-24035
	caBundleFilePath := filepath.Join(hostPath, common.CABundleFilePath)
	r.Log.Info("Copying", "file", caBundleFilePath)
	if err := utils.CopyFileIfExists(caBundleFilePath, filepath.Join(clusterConfigPath, filepath.Base(caBundleFilePath))); err != nil {
		return fmt.Errorf("failed to copy ca-bundle file %s to %s, err %w", caBundleFilePath, clusterConfigPath, err)
	}

	return nil
}

// gather network files and copy them
func (r *UpgradeClusterConfigGather) fetchNetworkConfig(ostreeDir string) error {
	r.Log.Info("Fetching node network files")
	dir := filepath.Join(ostreeDir, filepath.Join(common.OptOpenshift, common.NetworkDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create network folder %s, err %w", dir, err)
	}

	for _, path := range listOfNetworkFilesPaths {
		r.Log.Info("Copying network files", "file", path, "to", dir)
		err := utils.CopyFileIfExists(filepath.Join(hostPath, path), filepath.Join(dir, filepath.Base(path)))
		if err != nil {
			return fmt.Errorf("failed to copy %s to %s, err %w", path, dir, err)
		}
	}
	r.Log.Info("Done fetching node network files")
	return nil
}
