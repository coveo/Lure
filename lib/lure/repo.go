package lure

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"reflect"

	"github.com/vsekhar/govtil/guid"
)

// This part interesting
// https://github.com/golang/go/blob/1441f76938bf61a2c8c2ed1a65082ddde0319633/src/cmd/go/vcs.go

func appendIfMissing(modules []moduleVersion, modulesToAdd []moduleVersion) []moduleVersion {
	for _, moduleToAdd := range modulesToAdd {
		exist := false
		for _, module := range modules {
			if (moduleToAdd.Name != "" && module.Name == moduleToAdd.Name) || reflect.DeepEqual(module, moduleToAdd) {
				exist = true
				break
			}
		}
		if !exist {
			modules = append(modules, moduleToAdd)
		}
	}

	return modules
}

func cloneRepo(hgAuth Authentication, project Project) (Repo, error) {
	repoGUID, err := guid.V4()

	var repo Repo
	if err != nil {
		log.Printf("Error: \"Could not generate guid\" %s", err)
		return repo, err
	}
	repoPath := "/tmp/" + repoGUID.String()

	projectRemote := "https://bitbucket.org/" + project.Owner + "/" + project.Name

	log.Printf("Info: cloning: %s to %s", projectRemote, repoPath)

	switch project.Vcs {
	case Hg:
		repo, err = HgClone(hgAuth, projectRemote, repoPath)
	case Git:
		repo, err = GitClone(hgAuth, projectRemote, repoPath)
	default:
		repo = nil
		err = errors.New(fmt.Sprintf("Unknown VCS '%s' - must be one of %s, %s", project.Vcs, Git, Hg))
	}
	if err != nil {
		log.Printf("Error: \"Could not clone\" %s", err)
		return repo, err
	}

	return repo, nil
}

func CheckForUpdatesJobCommand(auth Authentication, project Project, args map[string]string) error {
	return checkForUpdatesJob(auth, project)
}

func checkForUpdatesJob(auth Authentication, project Project) error {

	repo, err := cloneRepo(auth, project)
	if err != nil {
		return err
	}

	log.Printf("Info: switching %s to default branch: %s", repo.RemotePath(), project.DefaultBranch)
	if _, err := repo.Update(project.DefaultBranch); err != nil {
		return errors.New(fmt.Sprintf("Error: \"Could not switch to branch %s\" %s", project.DefaultBranch, err))
	}

	modulesToUpdate := make([]moduleVersion, 0, 0)
	modulesToUpdate = appendIfMissing(modulesToUpdate, npmOutdated(repo.LocalPath() + "/" + project.BasePath))
	modulesToUpdate = appendIfMissing(modulesToUpdate, mvnOutdated(repo.LocalPath() + "/" + project.BasePath))
	log.Printf("Modules to update : %q", modulesToUpdate)
	pullRequests := getPullRequests(auth, project.Owner, project.Name)

	for _, moduleToUpdate := range modulesToUpdate {
		updateModule(auth, moduleToUpdate, project, repo, pullRequests)
	}

	return nil
}

func updateModule(auth Authentication, moduleToUpdate moduleVersion, project Project, repo Repo, existingPRs []PullRequest) {
	var title string
	var dependencyName string
	if moduleToUpdate.Name != "" {
		dependencyName = moduleToUpdate.Name
	} else {
		dependencyName = moduleToUpdate.Module
	}
	title = fmt.Sprintf("Update %s dependency %s to version %s", moduleToUpdate.Type, dependencyName, moduleToUpdate.Latest)
	for _, pr := range existingPRs {
		if pr.Title == title {
			log.Printf("There already is a PR for: %s", title)
			return
		}
	}

	log.Printf("Info: switching %s to default branch: %s", repo.LocalPath(), project.DefaultBranch)
	if _, err := repo.Update(project.DefaultBranch); err != nil {
		log.Fatalf("Error: \"Could not switch to branch %s\" %s", project.DefaultBranch, err)
	}

	branchGUID, _ := guid.V4()
	branchPrefix := project.BranchPrefix
	if branchPrefix == "" {
		branchPrefix = "lure-"
	}
	var branch = HgSanitizeBranchName(branchPrefix + dependencyName + "-" + moduleToUpdate.Latest + "-" + branchGUID.String())
	log.Printf("Creating branch %s\n", branch)
	if _, err := repo.Branch(branch); err != nil {
		log.Printf("Error: \"Could not create branch\" %s", err)
		return
	}

	hasChanges := false

	switch moduleToUpdate.Type {
	case "maven":
		hasChanges, _ = mvnUpdateDep(repo.LocalPath(), moduleToUpdate)
	case "npm":
		hasChanges, _ = readPackageJSON(repo.LocalPath(), moduleToUpdate.Module, moduleToUpdate.Latest)
	}

	if hasChanges == false {
		return
	}

	if _, err := repo.Commit("Update "+dependencyName+" to "+moduleToUpdate.Latest); err != nil {
		log.Printf("Error: \"Could not commit\" %s", err)
		return
	}

	if os.Getenv("DRY_RUN") == "1" {
		log.Println("Running in DryRun mode, not doing the pull request nor pushing the changes")
	} else {
		log.Printf("Pushing changes\n")
		if _, err := repo.Push(); err != nil {
			log.Fatalf("Error: \"Could not push\" %s", err)
			return
		}

		log.Printf("Creating PR\n")
		description := fmt.Sprintf("%s version %s is now available! Please update.", moduleToUpdate.Module, moduleToUpdate.Latest)
		createPullRequest(auth, branch, project.DefaultBranch, project.Owner, project.Name, title, description)
	}
}

func Execute(pwd string, command string, params ...string) (string, error) {
	log.Printf("%s %q\n", command, params)

	cmd := exec.Command(command, params...)
	cmd.Dir = pwd

	var buff bytes.Buffer
	var stderr bytes.Buffer

	cmd.Stdout = &buff
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Println(stderr.String())
		return "", err
	}

	out := buff.String()

	log.Printf("\t%s\n", out)

	return out, nil
}
