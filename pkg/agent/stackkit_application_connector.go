package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

func stackKitConnectorArgs(release stackkitrelease.Release, workDir string, c *agentpb.StackKitCommand) ([]string, []string, error) {
	if err := stackkitcommand.ValidateCommand(c); err != nil {
		return nil, nil, err
	}
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return nil, nil, err
	}
	op := stackkitcommand.ApplicationConnectorOperationName(c.Operation)
	if err = catalog.Dispatchable(op); err != nil {
		return nil, nil, err
	}
	dir := filepath.Join(workDir, ".stackkit")
	if err = os.MkdirAll(dir, 0750); err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("connector metadata directory is not confined to its workspace")
	}
	path, err := writeStackKitPrivateFile(workDir, ".techstack-connector-*.json", c.ApplicationConnectorRequestJson)
	if err != nil {
		return nil, nil, err
	}
	files := []string{path}
	argv, err := catalog.RenderArgv(op, map[string]string{"connectorRequestFile": relativeStackKitPath(workDir, path), "operationId": c.ApplicationOperationId})
	if err != nil {
		removeStackKitTempFiles(files)
		return nil, nil, err
	}
	return argv, files, nil
}
