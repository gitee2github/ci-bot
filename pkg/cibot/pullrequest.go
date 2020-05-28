package cibot

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"strings"

	"gitee.com/openeuler/go-gitee/gitee"
	"github.com/antihax/optional"
	"github.com/golang/glog"
)

// HandlePullRequestEvent handles pull request event
func (s *Server) HandlePullRequestEvent(event *gitee.PullRequestEvent) {
	if event == nil {
		return
	}

	glog.Infof("pull request sender: %v", event.Sender.Login)

	//validate commit numbers
	if err := s.ValidateCommits(event); err != nil {
		glog.Error("failed to validate pr commits ", err)
	}

	// handle events
	switch *event.Action {
	case "open":
		glog.Info("received a pull request open event")

		// add comment
		body := gitee.PullRequestCommentPostParam{}
		body.AccessToken = s.Config.GiteeToken
		body.Body = fmt.Sprintf(tipBotMessage, event.Sender.Login, s.Config.CommunityName, s.Config.CommunityName,
			s.Config.BotName, s.Config.CommandLink)
		owner := event.Repository.Namespace
		repo := event.Repository.Name
		number := event.PullRequest.Number
		_, _, err := s.GiteeClient.PullRequestsApi.PostV5ReposOwnerRepoPullsNumberComments(s.Context, owner, repo, number, body)
		if err != nil {
			glog.Errorf("unable to add comment in pull request: %v", err)
		}

		err = s.CheckCLAByPullRequestEvent(event)
		if err != nil {
			glog.Errorf("failed to check cla by pull request event: %v", err)
		}

		diff := s.CheckSpecialFileHasModified(event, s.Config.AccordingFile)
		if diff == "" {
			return
		}
		prjnames := ParseDiffInfoAndGetProjectName(diff)
		if 0 == len(prjnames) {
			glog.Infof("No project file need to add.")
			return
		}

		newfilerepo := s.Config.NewFileRepo
		newfilebranch := s.Config.NewFileBranch
		newowner := s.Config.NewFileOwner
		for _, prjn := range prjnames {
			exist := s.CheckWetherNewItemInObsProjects(event, prjn, newfilebranch, newfilerepo, newowner)
			if true == exist {
				glog.Infof("Project(%v) is in obs already.", prjn)
				continue
			}
			// send note
			s.SendNote4AutomaticNewFile(event)
		}
	case "update":
		glog.Info("received a pull request update event")

		// get pr info
		owner := event.Repository.Namespace
		repo := event.Repository.Name
		number := event.PullRequest.Number
		lvos := &gitee.GetV5ReposOwnerRepoPullsNumberOpts{}
		lvos.AccessToken = optional.NewString(s.Config.GiteeToken)
		pr, _, err := s.GiteeClient.PullRequestsApi.GetV5ReposOwnerRepoPullsNumber(s.Context, owner, repo, number, lvos)
		if err != nil {
			glog.Errorf("unable to get pull request. err: %v", err)
			return
		}
		listofPrLabels := pr.Labels
		glog.Infof("List of pr labels: %v", listofPrLabels)

		// remove lgtm if changes happen
		if s.hasLgtmLabel(pr.Labels) {
			err = s.CheckLgtmByPullRequestUpdate(event)
			if err != nil {
				glog.Errorf("check lgtm by pull request update. err: %v", err)
				return
			}
		}
	case "merge":
		glog.Info("Received a pull request merge event")

		diff := s.CheckSpecialFileHasModified(event, s.Config.AccordingFile)
		if diff == "" {
			return
		}
		prjnames := ParseDiffInfoAndGetProjectName(diff)
		if 0 == len(prjnames) {
			glog.Infof("No project file need to add.")
			return
		}

		newfilerepo := s.Config.NewFileRepo
		newfilebranch := s.Config.NewFileBranch
		newowner := s.Config.NewFileOwner
		for _, prjn := range prjnames {
			exist := s.CheckWetherNewItemInObsProjects(event, prjn, newfilebranch, newfilerepo, newowner)
			if true == exist {
				glog.Infof("Project(%v) is in obs already.", prjn)
				continue
			}
			// new a project file automaticly
			glog.Infof("Begin to create new project file, project name:%v.", prjn)
			_servicepath, _servicecontent := s.FillServicePathAndContentWithProjectName(prjn)
			s.NewFileWithPathAndContentInPullRequest(event, _servicepath, _servicecontent, newfilebranch, newfilerepo, newowner)
		}
	}
}
func (s *Server) SendNote4AutomaticNewFile(event *gitee.PullRequestEvent) {
	if event == nil {
		return
	}

	owner := event.Repository.Namespace
	repo := event.Repository.Name
	number := event.PullRequest.Number
	body := gitee.PullRequestCommentPostParam{}
	body.AccessToken = s.Config.GiteeToken
	body.Body = AutoAddPrjMsg + s.Config.GuideURL
	glog.Infof("Send notify info: %v.", body.Body)
	_, _, err := s.GiteeClient.PullRequestsApi.PostV5ReposOwnerRepoPullsNumberComments(s.Context, owner, repo, number, body)
	if err != nil {
		glog.Errorf("unable to add comment in pull request: %v", err)
	}
	return
}

// parse diff info
func ParseDiffInfoAndGetProjectName(diff string) (prjnames []string) {
	if strings.Contains(diff, "+- name:") {
		difs := strings.Fields(diff)
		for idx, str := range difs {
			// glog.Infof(str)
			if idx+2 >= len(difs) {
				break
			}
			if (str == "+-") && (difs[idx+1] == "name:") {
				prjnames = append(prjnames, difs[idx+2])
				glog.Infof(prjnames[0])
			}
		}
	}
	return
}

// Get the diff info with merge and choose projects to be added
func (s *Server) CheckSpecialFileHasModified(event *gitee.PullRequestEvent, specialfile string) (diff string) {
	diff = ""
	if event == nil {
		return
	}
	// get pr commit file list, community repo
	owner := event.Repository.Namespace
	repo := event.Repository.Name
	number := event.PullRequest.Number
	lvos := &gitee.GetV5ReposOwnerRepoPullsNumberFilesOpts{}
	lvos.AccessToken = optional.NewString(s.Config.GiteeToken)
	fls, _, err := s.GiteeClient.PullRequestsApi.GetV5ReposOwnerRepoPullsNumberFiles(s.Context, owner, repo, number, lvos)
	if err != nil {
		glog.Errorf("unable to get pr file list. err: %v", err)
		return
	}
	// check special file has modified and get diff
	for _, file := range fls {
		if strings.Contains(file.Filename, specialfile) {
			glog.Infof("%v has been modified", specialfile)
			diff = file.Patch.Diff
			break
		}
	}
	return
}

// Check whether the new item in src-openeuler.yaml is in project
func (s *Server) CheckWetherNewItemInObsProjects(event *gitee.PullRequestEvent, prjname string, branch string, repo string, owner string) (exist bool) {
	exist = false
	if event == nil {
		return
	}

	// get the sha of branch
	lvosbranch := &gitee.GetV5ReposOwnerRepoBranchesBranchOpts{}
	lvosbranch.AccessToken = optional.NewString(s.Config.GiteeToken)
	bdetail, _, err := s.GiteeClient.RepositoriesApi.GetV5ReposOwnerRepoBranchesBranch(s.Context, owner, repo, branch, lvosbranch)
	if err != nil {
		glog.Errorf("Get branch(%v) repo(%v) detail info failed: %v", branch, repo, err)
		return
	}

	// look up the obs project in infrastructure
	treesha := bdetail.Commit.Sha
	lvostree := &gitee.GetV5ReposOwnerRepoGitTreesShaOpts{}
	lvostree.AccessToken = optional.NewString(s.Config.GiteeToken)
	lvostree.Recursive = optional.NewInt32(1)
	tree, _, err := s.GiteeClient.GitDataApi.GetV5ReposOwnerRepoGitTreesSha(s.Context, owner, repo, treesha, lvostree)
	if err != nil {
		glog.Errorf("Get menu tree of branch(%v) repo(%v) failed: %v", branch, repo, err)
		return
	}
	for _, dir := range tree.Tree {
		if strings.Contains(dir.Path, "/"+prjname+"/") {
			glog.Infof("Find the project path:%v, sha:%v", dir.Path, dir.Sha)
			exist = true
		}
	}
	return
}

// Fill file _service path and content
func (s *Server) FillServicePathAndContentWithProjectName(prjname string) (_servicepath string, _service string) {
	_servicepath = strings.Replace(s.Config.ServicePath, "#projectname#", prjname, 1)
	glog.Infof("service path:%v", _servicepath)

	// read template file info
	filebuf, err := ioutil.ReadFile(s.Config.ServiceFile)
	if err != nil {
		glog.Errorf("Read template service file failed: %v.", err)
		return
	}
	str := string(filebuf)
	_service = strings.Replace(str, "#projectname#", prjname, 1)
	glog.Infof("service file:%v", _service)
	return
}

// New project with name in pull
func (s *Server) NewFileWithPathAndContentInPullRequest(event *gitee.PullRequestEvent, path string, content string, branch string, repo string, owner string) {
	if event == nil {
		return
	}
	newfbody := gitee.NewFileParam{}
	newfbody.AccessToken = s.Config.GiteeToken
	newfbody.AuthorName = event.PullRequest.User.Login
	newfbody.AuthorEmail = event.PullRequest.User.Email
	newfbody.CommitterName = event.PullRequest.User.Login
	newfbody.CommitterEmail = event.PullRequest.User.Email
	newfbody.Branch = branch
	newfbody.Message = "add project according to src-openeuler.yaml in repo community."

	glog.Infof("Begin to write template file (%v) autoly.", path)
	contentbase64 := base64.StdEncoding.EncodeToString([]byte(content))
	newfbody.Content = contentbase64
	_, _, err := s.GiteeClient.RepositoriesApi.PostV5ReposOwnerRepoContentsPath(s.Context, owner, repo, path, newfbody)
	if err != nil {
		glog.Errorf("New service file failed: %v.", err)
	}
	return
}

// RemoveAssigneesInPullRequest remove assignees in pull request
func (s *Server) RemoveAssigneesInPullRequest(event *gitee.NoteEvent) error {
	if event != nil {
		if event.PullRequest != nil {
			assignees := event.PullRequest.Assignees
			glog.Infof("remove assignees: %v", assignees)
			if len(assignees) > 0 {
				var strAssignees string
				for _, assignee := range assignees {
					strAssignees += assignee.Login + ","
				}
				strAssignees = strings.TrimRight(strAssignees, ",")
				glog.Infof("remove assignees str: %s", strAssignees)

				// get basic params
				owner := event.Repository.Namespace
				repo := event.Repository.Name
				prNumber := event.PullRequest.Number
				localVarOptionals := &gitee.DeleteV5ReposOwnerRepoPullsNumberAssigneesOpts{}
				localVarOptionals.AccessToken = optional.NewString(s.Config.GiteeToken)

				// invoke api
				_, _, err := s.GiteeClient.PullRequestsApi.DeleteV5ReposOwnerRepoPullsNumberAssignees(s.Context, owner, repo, prNumber, strAssignees, localVarOptionals)
				if err != nil {
					glog.Errorf("unable to remove assignees in pull request. err: %v", err)
					return err
				}
				glog.Infof("remove assignees successfully: %s", strAssignees)
			}
		}
	}
	return nil
}

// RemoveTestersInPullRequest remove testers in pull request
func (s *Server) RemoveTestersInPullRequest(event *gitee.NoteEvent) error {
	if event != nil {
		if event.PullRequest != nil {
			testers := event.PullRequest.Testers
			glog.Infof("remove testers: %v", testers)
			if len(testers) > 0 {
				var strTesters string
				for _, tester := range testers {
					strTesters += tester.Login + ","
				}
				strTesters = strings.TrimRight(strTesters, ",")
				glog.Infof("remove testers str: %s", strTesters)

				// get basic params
				owner := event.Repository.Namespace
				repo := event.Repository.Name
				prNumber := event.PullRequest.Number
				localVarOptionals := &gitee.DeleteV5ReposOwnerRepoPullsNumberTestersOpts{}
				localVarOptionals.AccessToken = optional.NewString(s.Config.GiteeToken)

				// invoke api
				_, _, err := s.GiteeClient.PullRequestsApi.DeleteV5ReposOwnerRepoPullsNumberTesters(s.Context, owner, repo, prNumber, strTesters, localVarOptionals)
				if err != nil {
					glog.Errorf("unable to remove testers in pull request. err: %v", err)
					return err
				}
				glog.Infof("remove testers successfully: %s", strTesters)
			}
		}
	}
	return nil
}

func (s *Server) hasLgtmLabel(labels []gitee.Label) bool {
	for _, l := range labels {
		if strings.HasPrefix(l.Name, fmt.Sprintf(LabelLgtmWithCommenter, "")) || l.Name == LabelNameLgtm {
			return true
		}
	}
	return false
}

func (s *Server) legalForMerge(labels []gitee.Label) bool {
	aproveLabel := 0
	lgtmLabel := 0
	lgtmPrefix := ""
	leastLgtm := 0
	if s.Config.LgtmCountsRequired > 1 {
		leastLgtm = s.Config.LgtmCountsRequired
		lgtmPrefix = fmt.Sprintf(LabelLgtmWithCommenter, "")
	} else {
		leastLgtm = 1
		lgtmPrefix = LabelNameLgtm
	}
	for _, l := range labels {
		if strings.HasPrefix(l.Name, lgtmPrefix) {
			lgtmLabel++
		} else if l.Name == LabelNameApproved {
			aproveLabel++
		}
	}
	glog.Infof("Pr labels have approved: %d lgtm: %d, required (%d)", aproveLabel, lgtmLabel, leastLgtm)
	return aproveLabel == 1 && lgtmLabel >= leastLgtm
}

// MergePullRequest with lgtm and approved label
func (s *Server) MergePullRequest(event *gitee.NoteEvent) error {
	// get basic params
	owner := event.Repository.Namespace
	repo := event.Repository.Name
	prNumber := event.PullRequest.Number
	glog.Infof("merge pull request started. owner: %s repo: %s number: %d", owner, repo, prNumber)

	// list labels in current pull request
	lvos := &gitee.GetV5ReposOwnerRepoPullsNumberOpts{}
	lvos.AccessToken = optional.NewString(s.Config.GiteeToken)
	pr, _, err := s.GiteeClient.PullRequestsApi.GetV5ReposOwnerRepoPullsNumber(s.Context, owner, repo, prNumber, lvos)
	if err != nil {
		glog.Errorf("unable to get pull request. err: %v", err)
		return err
	}
	listofPrLabels := pr.Labels
	glog.Infof("List of pr labels: %v", listofPrLabels)

	// ready to merge
	if s.legalForMerge(listofPrLabels) {
		// current pr can be merged
		if event.PullRequest.Mergeable {
			// remove assignees
			err = s.RemoveAssigneesInPullRequest(event)
			if err != nil {
				glog.Errorf("unable to remove assignees. err: %v", err)
				return err
			}
			// remove testers
			err = s.RemoveTestersInPullRequest(event)
			if err != nil {
				glog.Errorf("unable to remove testers. err: %v", err)
				return err
			}
			// merge pr
			body := gitee.PullRequestMergePutParam{}
			body.AccessToken = s.Config.GiteeToken
			_, err = s.GiteeClient.PullRequestsApi.PutV5ReposOwnerRepoPullsNumberMerge(s.Context, owner, repo, prNumber, body)
			if err != nil {
				glog.Errorf("unable to merge pull request. err: %v", err)
				return err
			}
		}
	}

	return nil
}
