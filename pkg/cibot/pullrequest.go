package cibot

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"strings"

	"gitee.com/openeuler/ci-bot/pkg/cibot/database"
	"gitee.com/openeuler/go-gitee/gitee"
	"github.com/antihax/optional"
	"github.com/golang/glog"
)

const (
	cannotMergeMessage = `This pull request can not be merged, you can try it again when label requirement meets. :astonished:
%s`
	nonRequiringLabelsMessage = ` Labels [**%s**] need to be added.`
	nonMissingLabelsMessage   = ` Labels [**%s**] need to be removed.`
)

// HandlePullRequestEvent handles pull request event
func (s *Server) HandlePullRequestEvent(actionDesc string, event *gitee.PullRequestEvent) {
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

		// add a tag to describe the sig name of the repo.
		sigName := s.getSigNameFromRepo(event.Repository.FullName)
		if len(sigName) > 0 {
                        addlabel := &gitee.NoteEvent{}
			addlabel.PullRequest = event.PullRequest
			addlabel.Repository = event.Repository
			addlabel.Comment = &gitee.NoteHook{}
			errors := s.AddSpecifyLabelsInPulRequest(addlabel, []string{fmt.Sprintf("sig/%s", sigName)}, true)
                        if errors != nil {
                                glog.Errorf("Add special label sig info failed: %v", errors)
                        }
		}


		//get committor list:
		var ps []database.Privileges
		err := database.DBConnection.Model(&database.Privileges{}).
			Where("owner = ? and repo = ? and type = ?", event.Repository.Namespace, event.Repository.Path, PrivilegeDeveloper).Find(&ps).Error
		if err != nil {
			glog.Errorf("unable to get members: %v", err)
		}
		var committors []string
		if len(ps) > 0 {
			for _, p := range ps {
				if len(committors) < 10 {
					committors = append(committors, fmt.Sprintf("***@%s***", p.User))
				}
			}
		}
		committor_list := strings.Join(committors, ", ")
		if len(committor_list) > 0 {
			sigPath := fmt.Sprintf(SigPath, sigName)
			proInfo := fmt.Sprintf(DisplayCommittors, sigName, sigPath)
			committor_list = proInfo + committor_list + "."
		}

		// add comment
		body := gitee.PullRequestCommentPostParam{}
		body.AccessToken = s.Config.GiteeToken
		body.Body = fmt.Sprintf(tipBotMessage, event.Sender.Login, s.Config.CommunityName, s.Config.CommandLink, committor_list)
		owner := event.Repository.Namespace
		repo := event.Repository.Path
		number := event.PullRequest.Number
		_, _, err = s.GiteeClient.PullRequestsApi.PostV5ReposOwnerRepoPullsNumberComments(s.Context, owner, repo, number, body)
		if err != nil {
			glog.Errorf("unable to add comment in pull request: %v", err)
		}

		if s.Config.CheckPrReviewer {
			if !s.checkPrHasSetReviewer(event) {
				body.Body = fmt.Sprintf(" ***@%s*** %s", event.Sender.Login, s.Config.SetReviewerTip)
				_, _, err = s.GiteeClient.PullRequestsApi.PostV5ReposOwnerRepoPullsNumberComments(s.Context, owner, repo, number, body)
				if err != nil {
					glog.Errorf("unable to add comment in pull request: %v", err)
				}
			}
		}

		if s.Config.AutoDetectCla {
			err = s.CheckCLAByPullRequestEvent(event)
			if err != nil {
				glog.Errorf("failed to check cla by pull request event: %v", err)
			}
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
		needreport := false
		for _, prjn := range prjnames {
			exist := s.CheckWetherNewItemInObsProjects(event, prjn, newfilebranch, newfilerepo, newowner)
			if true == exist {
				glog.Infof("Project(%v) is in obs already.", prjn)
				continue
			}
			needreport = true
		}

		// send note
		if needreport {
			s.SendNote4AutomaticNewFile(event)
		}

	case "update":
		glog.Info("received a pull request update event")

		// get pr info
		owner := event.Repository.Namespace
		repo := event.Repository.Path
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
		// remove labels if action_desc is "source_branch_changed"
		if len(pr.Labels) == 0 || actionDesc != s.Config.PrUpdateLabelFlag {
			return
		}
		delLabels, updateLabels := GetChangeLabels(s.Config.DelLabels, pr.Labels)
		if len(delLabels) == 0 {
			// Add retest comment for update push.
			retest_comment := "/retest"
			cBody := gitee.PullRequestCommentPostParam{}
			cBody.AccessToken = s.Config.GiteeToken
			cBody.Body = fmt.Sprintf(retest_comment)
			_, _, err = s.GiteeClient.PullRequestsApi.PostV5ReposOwnerRepoPullsNumberComments(s.Context, owner, repo, number, cBody)
			if err != nil{
				glog.Info("Add retest comment failed. err: %v", err)
			}
			return
		}
		err = s.UpdateLabelsBySourceBranchChange(delLabels, updateLabels, event)
		if err != nil {
			glog.Info(err)
		}
		// Add retest comment for update push.
		retest_comment := "/retest"
		cBody := gitee.PullRequestCommentPostParam{}
		cBody.AccessToken = s.Config.GiteeToken
		cBody.Body = fmt.Sprintf(retest_comment)
		_, _, err = s.GiteeClient.PullRequestsApi.PostV5ReposOwnerRepoPullsNumberComments(s.Context, owner, repo, number, cBody)
		if err != nil{
			glog.Info("Add retest comment failed. err: %v", err)
		}
		// remove lgtm if changes happen
		/*if s.hasLgtmLabel(pr.Labels) {
			err = s.CheckLgtmByPullRequestUpdate(event)
			if err != nil {
				glog.Errorf("check lgtm by pull request update. err: %v", err)
				return
			}
		}*/
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

func (s *Server) UpdateLabelsBySourceBranchChange(delLabels, updateLabels []string, event *gitee.PullRequestEvent) error {
	owner := event.Repository.Namespace
	repo := event.Repository.Path
	prNumber := event.PullRequest.Number
	strLabel := strings.Join(updateLabels, ",")
	strDelLabel := strings.Join(delLabels, ",")
	body := gitee.PullRequestUpdateParam{}
	body.AccessToken = s.Config.GiteeToken
	body.Labels = strLabel
	glog.Infof("invoke api to remove labels: %v", strLabel)
	//update pr
	_, response, err := s.GiteeClient.PullRequestsApi.PatchV5ReposOwnerRepoPullsNumber(s.Context, owner, repo, prNumber, body)
	if err != nil {
		if response != nil && response.StatusCode == 400 {
			glog.Infof("remove labels successfully with status code %d: %v", response.StatusCode, strDelLabel)
		} else {
			glog.Errorf("unable to remove labels: %v err: %v", strDelLabel, err)
			return err
		}
	} else {
		glog.Infof("remove labels successfully: %v", strDelLabel)
	}
	// add comment for update labels
	commentContent := `new changes are detected. ***%s*** is removed in this pull request by: ***%s***. :flushed: `
	cBody := gitee.PullRequestCommentPostParam{}
	cBody.AccessToken = s.Config.GiteeToken
	cBody.Body = fmt.Sprintf(commentContent, strDelLabel, s.Config.BotName)
	_, _, err = s.GiteeClient.PullRequestsApi.PostV5ReposOwnerRepoPullsNumberComments(s.Context, owner, repo, prNumber, cBody)
	if err != nil {
		glog.Errorf("unable to add comment in pull request: %v", err)
		return err
	}
	return nil
}

func (s *Server) SendNote4AutomaticNewFile(event *gitee.PullRequestEvent) {
	if event == nil {
		return
	}

	owner := event.Repository.Namespace
	repo := event.Repository.Path
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
	if len(specialfile) == 0 || event == nil {
		return ""
	}
	diff = ""
	// get pr commit file list, community repo
	owner := event.Repository.Namespace
	repo := event.Repository.Path
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
				repo := event.Repository.Path
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
				repo := event.Repository.Path
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

func (s *Server) readyForMerge(labels []gitee.Label, owner, repo string) error {
	aproveLabel := 0
	lgtmLabel := 0
	lgtmPrefix := ""
	leastLgtm := 0
	count := s.calculateLgtmLabel(owner, repo)
	if count > 1 {
		leastLgtm = count
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
	if aproveLabel == 1 && lgtmLabel >= leastLgtm {
		return nil
	} else {
		return fmt.Errorf("This pull request can not be merged, please check that the number of **lgtm** labels >= %d "+
			"and there are an **approve** labels. ", leastLgtm)
	}
}

// check with the labels constraints requiring/missing to determine if mergable
func (s *Server) legalLabelsForMerge(labels []gitee.Label) ([]string, []string) {
	nonRequiring, _ := s.labelDiffer(s.Config.RequiringLabels, labels)
	_, nonMissing := s.labelDiffer(s.Config.MissingLabels, labels)

	return nonRequiring, nonMissing
}

// MergePullRequest with lgtm and approved label
func (s *Server) MergePullRequest(event *gitee.NoteEvent) error {
	// get basic params
	owner := event.Repository.Namespace
	repo := event.Repository.Path
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
	err = s.readyForMerge(listofPrLabels, owner, repo)
	if err != nil {
		return err
	}
	nonRequiringLabels, nonMissingLabels := s.legalLabelsForMerge(listofPrLabels)
	if len(nonRequiringLabels) == 0 && len(nonMissingLabels) == 0 {
		// current pr can be merged
		if c, b := checkFrozenCanMerge(event.Author.Login, pr.Base.Ref, owner); !b {
			//send comment to pr
			comment := ""
			if len(c) > 0 {
				comment = fmt.Sprintf("**Merge failed** The current pull request merge target has been frozen, and only the branch owner( @%s ) can merge.",
					strings.Join(c, " , @"))
			} else {
				comment = "**Merge failed** The current pull request merge target has been frozen, and only the branch owner can merge."
			}
			err = s.addCommentToPullRequest(owner, repo, comment, prNumber)
			if err != nil {
				glog.Errorf("Cannot add comments to pull request: %v", err)
			}
		} else {
			if event.PullRequest.Mergeable {
				// remove assignees
				err = s.RemoveAssigneesInPullRequest(event)
				if err != nil {
					glog.Errorf("unable to remove assignees. err: %v", err)
				}
				// remove testers
				err = s.RemoveTestersInPullRequest(event)
				if err != nil {
					glog.Errorf("unable to remove testers. err: %v", err)
				}
				// merge pr
				body := gitee.PullRequestMergePutParam{}
				body.AccessToken = s.Config.GiteeToken
				// generate merge body
				description, err := s.generateMergeDescription(event)
				if err != nil {
					glog.Errorf("unable to get merge description.err: %v", err)
					return fmt.Errorf(`The pull request merge failed, please use command "/check-pr" to try again. `)
				}
				body.Description = description

				_, err = s.GiteeClient.PullRequestsApi.PutV5ReposOwnerRepoPullsNumberMerge(s.Context, owner, repo, prNumber, body)
				if err != nil {
					glog.Errorf("unable to merge pull request. err: %v", err)
					return fmt.Errorf(`The pull request merge failed, please use command "/check-pr" to try again. `)
				}
			}
		}
	} else {
		// add comment to pr to show the labels reason of not mergable
		nonRequiringMsg := ""
		if len(nonRequiringLabels) > 0 {
			nonRequiringMsg = fmt.Sprintf(nonRequiringLabelsMessage, strings.Join(nonRequiringLabels, ","))
		}
		nonMissingMsg := ""
		if len(nonMissingLabels) > 0 {
			nonMissingMsg = fmt.Sprintf(nonMissingLabelsMessage, strings.Join(nonMissingLabels, ","))
		}
		// add comment back to pr
		comment := fmt.Sprintf(cannotMergeMessage, fmt.Sprintf("%s%s", nonRequiringMsg, nonMissingMsg))
		owner := event.Repository.Namespace
		repo := event.Repository.Path
		number := event.PullRequest.Number
		err = s.addCommentToPullRequest(owner, repo, comment, number)
		if err != nil {
			glog.Errorf("unable to add comment in pull request: %v", err)
		}
	}
	return nil
}

func (s *Server) generateMergeDescription(event *gitee.NoteEvent) (string, error) {
	// get basic params
	owner := event.Repository.Namespace
	repo := event.Repository.Path
	prNumber := event.PullRequest.Number
	commentCount := event.PullRequest.Comments
	user := event.PullRequest.User.Login
	var perPage int32 = 20
	pageCount := commentCount / perPage
	if commentCount%perPage > 0 {
		pageCount++
	}

	result := ""
	localVarOptionals := &gitee.GetV5ReposOwnerRepoPullsNumberCommentsOpts{}
	localVarOptionals.AccessToken = optional.NewString(s.Config.GiteeToken)
	localVarOptionals.PerPage = optional.NewInt32(perPage)

	var signers = make([]string, 0)
	var reviewers = make([]string, 0)
	// range page and get comments
	for page := pageCount; page > 0; page-- {
		localVarOptionals.Page = optional.NewInt32(page)
		comments, _, err :=
			s.GiteeClient.PullRequestsApi.GetV5ReposOwnerRepoPullsNumberComments(s.Context, owner, repo, prNumber, localVarOptionals)
		if err != nil {
			glog.Errorf("unable to get pull request comments. err:%v", err)
			return result, err
		}

		signers, reviewers, err = getSignersAndReviewers(user, comments)
		if err != nil {
			glog.Errorf("failed to get signers or reviewers. err:%v", err)
		}
	}

	result = formatDescription(user, reviewers, signers)
	return result, nil
}

func formatDescription(user string, reviewers, signers []string) string {
	return fmt.Sprintf("From: @%s\nReviewed-by: %s\nSigned-off-by: %s\n",
		user, strings.Join(reviewers, ","),
		strings.Join(signers, ","))
}

func getSignersAndReviewers(user string, comments []gitee.PullRequestComments) ([]string, []string, error) {
	var signers = make([]string, 0)
	var reviewers = make([]string, 0)

	if len(comments) == 0 {
		return signers, reviewers, fmt.Errorf("comment list is empty")
	}

	for _, comment := range comments {
		m := RegAddLgtm.FindStringSubmatch(comment.Body)
		if m != nil && comment.UpdatedAt == comment.CreatedAt && comment.User.Login != user {
			reviewer := fmt.Sprintf("@%s", comment.User.Login)
			reviewers = append(reviewers, reviewer)
		}

		m = RegAddApprove.FindStringSubmatch(comment.Body)
		if m != nil && comment.UpdatedAt == comment.CreatedAt && comment.User.Login != user {
			signer := fmt.Sprintf("@%s", comment.User.Login)
			signers = append(signers, signer)
		}
	}

	return signers, reviewers, nil
}

func checkFrozenCanMerge(commenter, branch string, community string) ([]string, bool) {
	frozen, isFrozen := IsBranchFrozen(branch, community)
	if isFrozen {
		canMerge := false
		for _, v := range frozen {
			if v == commenter {
				canMerge = true
				break
			}
		}
		return frozen, canMerge
	} else {
		return frozen, true
	}
}

func (s *Server) checkPrHasSetReviewer(pre *gitee.PullRequestEvent) bool {
	if pre.PullRequest != nil && len(pre.PullRequest.Assignees) > 0 {
		return true
	} else {
		//get pr info
		owner := pre.Repository.Namespace
		repo := pre.Repository.Path
		number := pre.PullRequest.Number
		lvos := &gitee.GetV5ReposOwnerRepoPullsNumberOpts{}
		lvos.AccessToken = optional.NewString(s.Config.GiteeToken)
		pr, _, err := s.GiteeClient.PullRequestsApi.GetV5ReposOwnerRepoPullsNumber(s.Context, owner, repo, number, lvos)
		if err != nil {
			glog.Errorf("unable to get pull request. err: %v", err)
			return false
		}
		if len(pr.Assignees) > 0 {
			return true
		}
	}
	return false
}
