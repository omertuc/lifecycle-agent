package installationiso

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/openshift-kni/lifecycle-agent/lca-cli/ops"
	"github.com/openshift-kni/lifecycle-agent/utils"
	"github.com/sirupsen/logrus"
)

type InstallationIso struct {
	log     *logrus.Logger
	ops     ops.Ops
	workDir string
}

type IgnitionData struct {
	SeedImage         string
	SeedVersion       string
	BackupSecret      string
	PullSecret        string
	SshPublicKey      string
	InstallSeedScript string
	LCAImage          string
	InstallationDisk  string
}

//go:embed data/*
var folder embed.FS

func NewInstallationIso(log *logrus.Logger, ops ops.Ops, workDir string) *InstallationIso {
	return &InstallationIso{
		log:     log,
		ops:     ops,
		workDir: workDir,
	}
}

const (
	ibiButaneTemplateFilePath = "data/ibi-butane.template"
	seedInstallScriptFilePath = "data/install-rhcos-and-restore-seed.sh"
	butaneFiles               = "butaneFiles"
	butaneConfigFile          = "config.bu"
	ibiIgnitionFileName       = "ibi-ignition.json"
	rhcosIsoFileName          = "rhcos-live.x86_64.iso"
	ibiIsoFileName            = "rhcos-ibi.iso"
	coreosInstallerImage      = "quay.io/coreos/coreos-installer:latest"
)

func (r *InstallationIso) Create(seedImage, seedVersion, authFile, pullSecretFile, sshPublicKeyPath, lcaImage, rhcosLiveIsoUrl, installationDisk string) error {
	r.log.Info("Creating IBI installation ISO")
	err := r.validate()
	if err != nil {
		return err
	}
	err = r.createIgnitionFile(seedImage, seedVersion, authFile, pullSecretFile, sshPublicKeyPath, lcaImage, installationDisk)
	if err != nil {
		return err
	}
	if err := r.downloadLiveIso(rhcosLiveIsoUrl); err != nil {
		return err
	}
	if err := r.embedIgnitionToIso(); err != nil {
		return err
	}
	r.log.Infof("installation ISO created at: %s", path.Join(r.workDir, ibiIsoFileName))

	return nil
}

func (r *InstallationIso) validate() error {
	_, err := os.Stat(r.workDir)
	if err != nil && os.IsNotExist(err) {
		return fmt.Errorf("work dir doesn't exists %w", err)
	}
	return err
}

func (r *InstallationIso) createIgnitionFile(seedImage, seedVersion, authFile, pullSecretFile, sshPublicKeyPath, lcaImage, installationDisk string) error {
	r.log.Info("Generating Ignition Config")
	err := r.renderButaneConfig(seedImage, seedVersion, authFile, pullSecretFile, sshPublicKeyPath, lcaImage, installationDisk)
	if err != nil {
		return err
	}
	return r.renderIgnitionFile()
}

func (r *InstallationIso) renderIgnitionFile() error {
	ibiIsoPath := path.Join(r.workDir, ibiIgnitionFileName)
	if _, err := os.Stat(ibiIsoPath); err == nil {
		r.log.Infof("ignition file exists (%s), deleting it", ibiIsoPath)
		if err = os.Remove(ibiIsoPath); err != nil {
			return fmt.Errorf("failed to delete existing ignition config: %w", err)
		}
	}

	command := "podman"
	args := []string{"run",
		"-v", fmt.Sprintf("%s:/data:rw,Z", r.workDir),
		"--rm",
		"quay.io/coreos/butane:release",
		"--pretty", "--strict",
		"-d", "/data",
		path.Join("/data", butaneConfigFile),
	}
	ignitionContent, err := r.ops.RunInHostNamespace(command, args...)
	if err != nil {
		return fmt.Errorf("failed to render ignition from config: %w", err)
	}
	return os.WriteFile(path.Join(r.workDir, ibiIgnitionFileName), []byte(ignitionContent), 0o644)
}

func (r *InstallationIso) embedIgnitionToIso() error {
	ibiIsoPath := path.Join(r.workDir, ibiIsoFileName)
	if _, err := os.Stat(ibiIsoPath); err == nil {
		r.log.Infof("ibi ISO exists (%s), deleting it", ibiIsoPath)
		if err = os.Remove(ibiIsoPath); err != nil {
			return fmt.Errorf("failed to delete existing ibi ISO: %w", err)
		}
	}

	command := "podman"
	args := []string{"run",
		"-v", fmt.Sprintf("%s:/data:rw,Z", r.workDir),
		coreosInstallerImage,
		"iso", "ignition", "embed",
		"-i", path.Join("/data", ibiIgnitionFileName),
		"-o", path.Join("/data", ibiIsoFileName),
		path.Join("/data", rhcosIsoFileName),
	}

	if _, err := r.ops.RunInHostNamespace(command, args...); err != nil {
		return err
	}
	return nil
}

func (r *InstallationIso) renderButaneConfig(seedImage, seedVersion, authFile, pullSecretFile, sshPublicKeyPath, lcaImage, installationDisk string) error {
	r.log.Debug("Generating butane config")
	var sshPublicKey []byte
	var err error
	if sshPublicKeyPath == "" {
		r.log.Info("ssh key not provided skipping")
	} else {
		sshPublicKey, err = os.ReadFile(sshPublicKeyPath)
		if err != nil {
			return fmt.Errorf("failed to read ssh public key: %w", err)
		}
	}

	butaneDataDir := path.Join(r.workDir, butaneFiles)
	r.log.Debugf("Create %s directory for storing butane config files", butaneDataDir)
	os.Mkdir(butaneDataDir, 0o700)
	// We could apply the template data using the files content (referenced in the butane config as inline)
	// but that might result unmarshal errors while translating the config
	// hence we are copying the files to the butaneDataDir to be referenced as local files
	seedInstallScriptInButane := path.Join(butaneDataDir, "seedInstallScript")
	if err := r.copyFileToButaneDir(seedInstallScriptFilePath, seedInstallScriptInButane); err != nil {
		return err
	}
	pullSecretInButane := path.Join(butaneDataDir, "pullSecret")
	if err := r.copyFileToButaneDir(pullSecretFile, pullSecretInButane); err != nil {
		return err
	}
	backupSecretInButane := path.Join(butaneDataDir, "backupSecret")
	if err := r.copyFileToButaneDir(authFile, backupSecretInButane); err != nil {
		return err
	}

	templateData := IgnitionData{SeedImage: seedImage,
		SeedVersion:       seedVersion,
		BackupSecret:      removeFirstDirectory(backupSecretInButane),
		PullSecret:        removeFirstDirectory(pullSecretInButane),
		SshPublicKey:      string(sshPublicKey),
		InstallSeedScript: removeFirstDirectory(seedInstallScriptInButane),
		LCAImage:          lcaImage,
		InstallationDisk:  installationDisk}

	if err := utils.RenderTemplateFile(ibiButaneTemplateFilePath, templateData, path.Join(r.workDir, butaneConfigFile), 0o644); err != nil {
		return err
	}
	return nil
}

func removeFirstDirectory(path string) string {
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) > 1 {
		// Remove the first element (directory) and join the rest
		return filepath.Join(parts[1:]...)
	}
	return path
}

func (r *InstallationIso) copyFileToButaneDir(sourceFile, target string) error {
	var source fs.File
	var err error
	// this file isn't provided by the user, it's part of the data folder embedded into the go binary at the top of this file
	if sourceFile == seedInstallScriptFilePath {
		source, err = folder.Open(sourceFile)
	} else {
		source, err = os.Open(sourceFile)
	}

	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer source.Close()
	fileForButaneConfig, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("failed to create file under workdir: %w", err)
	}
	defer fileForButaneConfig.Close()
	if _, err = io.Copy(fileForButaneConfig, source); err != nil {
		return fmt.Errorf("failed to copy file to workdir: %w", err)
	}
	return nil
}

func (r *InstallationIso) downloadLiveIso(url string) error {
	r.log.Info("Downloading live ISO")
	rhcosIsoPath := path.Join(r.workDir, rhcosIsoFileName)
	if _, err := os.Stat(rhcosIsoPath); err == nil {
		r.log.Infof("rhcos live ISO (%s) exists, skipping download", rhcosIsoPath)
		return nil
	}

	isoFile, err := os.Create(rhcosIsoPath)
	if err != nil {
		return err
	}
	defer isoFile.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download ISO from URL, status: %s", resp.Status)
	}

	_, err = io.Copy(isoFile, resp.Body)
	return err
}
