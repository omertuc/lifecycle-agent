/*
Copyright 2023.

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

package cmd

import (
	"github.com/spf13/cobra"

	"github.com/openshift-kni/lifecycle-agent/internal/common"
	"github.com/openshift-kni/lifecycle-agent/lca-cli/ops"
	"github.com/openshift-kni/lifecycle-agent/lca-cli/seedrestoration"
)

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore seed cluster configurations",
	Run: func(cmd *cobra.Command, args []string) {
		restore()
	},
}

func init() {

	// Add restore command
	rootCmd.AddCommand(restoreCmd)

	// Add flags to restore command
	addCommonFlags(restoreCmd)
}

func restore() {
	log.Info("Restore operation has started")

	hostCommandsExecutor := ops.NewNsenterExecutor(log, true)
	op := ops.NewOps(log, hostCommandsExecutor)

	seedRestore := seedrestoration.NewSeedRestoration(log, op, common.BackupDir, containerRegistry,
		authFile, recertContainerImage, recertSkipValidation)

	if err := seedRestore.CleanupSeedCluster(); err != nil {
		log.Fatalf("Failed to restore seed cluster: %v", err)
	}

	log.Info("Seed cluster restored successfully!")
}
