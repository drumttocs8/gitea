// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package scada

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	repo_model "code.gitea.io/gitea/models/repo"
	user_model "code.gitea.io/gitea/models/user"
	"code.gitea.io/gitea/modules/git"
	"code.gitea.io/gitea/modules/git/gitcmd"
	"code.gitea.io/gitea/modules/graceful"
	"code.gitea.io/gitea/modules/log"
	"code.gitea.io/gitea/modules/process"
	"code.gitea.io/gitea/modules/queue"
	repo_module "code.gitea.io/gitea/modules/repository"
	"code.gitea.io/gitea/modules/setting"
	"code.gitea.io/gitea/modules/util"
	files_service "code.gitea.io/gitea/services/repository/files"
)

type RTACConfigJob struct {
	RepoID    int64
	Branch    string
	CommitID  string
	ExpPaths  []string
	PusherID  int64
	QueuedAt  time.Time
}

var rtacQueue *queue.WorkerPoolQueue[*RTACConfigJob]

func Init() error {
	if !setting.SCADA.Enabled {
		return nil
	}

	rtacQueue = queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), "rtac_config", handler)
	if rtacQueue == nil {
		return errors.New("unable to create rtac_config queue")
	}
	go graceful.GetManager().RunWithCancel(rtacQueue)
	return nil
}

func EnqueueRTACJobs(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, updates []*repo_module.PushUpdateOptions) error {
	if !setting.SCADA.Enabled {
		return nil
	}
	if rtacQueue == nil {
		return errors.New("rtac queue not initialized")
	}
	if repo == nil || gitRepo == nil || len(updates) == 0 {
		return nil
	}

	objectFormat, err := gitRepo.GetObjectFormat()
	if err != nil {
		return err
	}

	branchFiles := map[string][]string{}
	branchCommit := map[string]string{}
	branchPusher := map[string]int64{}

	for _, update := range updates {
		if !update.RefFullName.IsBranch() || update.IsDelRef() || git.IsEmptyCommitID(update.NewCommitID) {
			continue
		}
		baseCommit := update.OldCommitID
		if git.IsEmptyCommitID(baseCommit) {
			baseCommit = objectFormat.EmptyObjectID().String()
		}

		files, err := gitRepo.GetFilesChangedBetween(baseCommit, update.NewCommitID)
		if err != nil {
			log.Error("GetFilesChangedBetween failed for %s/%s: %v", repo.OwnerName, repo.Name, err)
			continue
		}
		filtered := filterExpFiles(files)
		if len(filtered) == 0 {
			continue
		}

		branch := update.RefFullName.BranchName()
		branchFiles[branch] = append(branchFiles[branch], filtered...)
		branchCommit[branch] = update.NewCommitID
		branchPusher[branch] = update.PusherID
	}

	for branch, expPaths := range branchFiles {
		uniquePaths := uniqueStrings(expPaths)
		job := &RTACConfigJob{
			RepoID:   repo.ID,
			Branch:   branch,
			CommitID: branchCommit[branch],
			ExpPaths: uniquePaths,
			PusherID: branchPusher[branch],
			QueuedAt: time.Now().UTC(),
		}
		if err := rtacQueue.Push(job); err != nil {
			return err
		}
	}

	return nil
}

func handler(items ...*RTACConfigJob) []*RTACConfigJob {
	for _, job := range items {
		if err := processJob(graceful.GetManager().ShutdownContext(), job); err != nil {
			log.Error("RTAC processing failed for repo %d: %v", job.RepoID, err)
		}
	}
	return nil
}

func processJob(ctx context.Context, job *RTACConfigJob) error {
	if job == nil || len(job.ExpPaths) == 0 {
		return nil
	}

	repo, err := repo_model.GetRepositoryByID(ctx, job.RepoID)
	if err != nil {
		return err
	}
	if job.Branch == "" {
		return fmt.Errorf("missing branch for RTAC job repo=%d", repo.ID)
	}

	t, err := files_service.NewTemporaryUploadRepository(repo)
	if err != nil {
		return err
	}
	defer t.Close()

	if err := t.Clone(ctx, job.Branch, false); err != nil {
		return err
	}
	if err := t.SetDefaultIndex(ctx); err != nil {
		return err
	}

	tempExpDir, err := os.MkdirTemp("", "rtac-exp-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(tempExpDir); err != nil {
			log.Warn("Failed to remove temp RTAC directory: %v", err)
		}
	}()

	filesCopied, err := copyExpFiles(t, tempExpDir, job.ExpPaths)
	if err != nil {
		return err
	}
	if filesCopied == 0 {
		return nil
	}

	scriptPath := filepath.Join(setting.SCADA.RTACPLGPath, "process_rtac_configs.py")
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("rtac-plg script not found: %w", err)
	}

	pythonPath := setting.SCADA.PythonPath
	if pythonPath == "" {
		pythonPath = "python"
	}

	env := append(os.Environ(), "ACRTACMD_PATH="+setting.SCADA.AcRtacCmdPath)
	desc := fmt.Sprintf("RTAC process: %s/%s (%s)", repo.OwnerName, repo.Name, job.Branch)
	_, stderr, err := process.GetManager().ExecDirEnv(ctx, setting.SCADA.CommandTimeout, filepath.Dir(scriptPath), desc, env, pythonPath, scriptPath, tempExpDir)
	if err != nil {
		return fmt.Errorf("rtac-plg failed: %w (stderr: %s)", err, stderr)
	}

	outputBase := filepath.Join(tempExpDir, "output")
	if err := applyOutputs(t.BasePath(), outputBase); err != nil {
		return err
	}

	if err := gitcmd.NewCommand("add", "-A", "xml", "points-lists").WithDir(t.BasePath()).RunWithStderr(ctx); err != nil {
		return err
	}

	status, _, err := gitcmd.NewCommand("status", "--porcelain").WithDir(t.BasePath()).RunStdString(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}

	lastCommitID, err := t.GetLastCommit(ctx)
	if err != nil {
		return err
	}

	treeHash, err := t.WriteTree(ctx)
	if err != nil {
		return err
	}

	commitMessage := fmt.Sprintf("Auto-generate RTAC XML and points list (%s)", job.Branch)
	doer := user_model.NewActionsUser()
	commitHash, err := t.CommitTree(ctx, &files_service.CommitTreeUserOptions{
		ParentCommitID: lastCommitID,
		TreeHash:       treeHash,
		CommitMessage:  commitMessage,
		DoerUser:       doer,
	})
	if err != nil {
		return err
	}

	return t.Push(ctx, doer, commitHash, job.Branch, false)
}

func copyExpFiles(t *files_service.TemporaryUploadRepository, tempExpDir string, paths []string) (int, error) {
	seen := map[string]struct{}{}
	count := 0
	for _, relPath := range paths {
		clean := filepath.Clean(filepath.FromSlash(relPath))
		src := filepath.Join(t.BasePath(), clean)
		if _, ok := seen[src]; ok {
			continue
		}
		seen[src] = struct{}{}

		if _, err := os.Stat(src); err != nil {
			log.Warn("Skipping missing .exp file: %s", src)
			continue
		}
		dest := filepath.Join(tempExpDir, filepath.Base(clean))
		if err := util.CopyFile(src, dest); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func applyOutputs(repoPath, outputBase string) error {
	entries, err := os.ReadDir(outputBase)
	if err != nil {
		return err
	}

	xmlRoot := filepath.Join(repoPath, "xml")
	pointsRoot := filepath.Join(repoPath, "points-lists")
	if err := os.MkdirAll(xmlRoot, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(pointsRoot, 0o755); err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projectName := entry.Name()
		projectDir := filepath.Join(outputBase, projectName)

		xmlSrc := filepath.Join(projectDir, "xml")
		xmlDest := filepath.Join(xmlRoot, projectName)
		if err := os.RemoveAll(xmlDest); err != nil {
			return err
		}
		if err := copyDir(xmlSrc, xmlDest); err != nil {
			return err
		}

		xlsxSrc := filepath.Join(projectDir, projectName+"_points.xlsx")
		xlsxDest := filepath.Join(pointsRoot, projectName+"_points.xlsx")
		if _, err := os.Stat(xlsxSrc); err != nil {
			return fmt.Errorf("missing points list output: %w", err)
		}
		if err := util.CopyFile(xlsxSrc, xlsxDest); err != nil {
			return err
		}
	}

	return nil
}

func copyDir(src, dest string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		destPath := filepath.Join(dest, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, destPath); err != nil {
				return err
			}
			continue
		}
		if err := util.CopyFile(srcPath, destPath); err != nil {
			return err
		}
	}
	return nil
}

func filterExpFiles(files []string) []string {
	filtered := make([]string, 0, len(files))
	for _, path := range files {
		lower := strings.ToLower(path)
		if strings.HasPrefix(lower, "exp/") && strings.HasSuffix(lower, ".exp") {
			filtered = append(filtered, path)
		}
	}
	return filtered
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
