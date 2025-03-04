package trace

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/profclems/glab/pkg/iostreams"

	"github.com/profclems/glab/internal/glrepo"
	"github.com/profclems/glab/pkg/prompt"

	"github.com/profclems/glab/api"
	"github.com/profclems/glab/commands/ci/ciutils"
	"github.com/profclems/glab/commands/cmdutils"
	"github.com/profclems/glab/pkg/git"
	"github.com/profclems/glab/pkg/utils"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/MakeNowJust/heredoc"
	"github.com/spf13/cobra"
	"github.com/xanzy/go-gitlab"
)

type TraceOpts struct {
	Branch string
	JobID  int

	BaseRepo   func() (glrepo.Interface, error)
	HTTPClient func() (*gitlab.Client, error)
	IO         *iostreams.IOStreams
}

func NewCmdTrace(f *cmdutils.Factory, runE func(traceOpts *TraceOpts) error) *cobra.Command {
	opts := &TraceOpts{
		IO: f.IO,
	}
	var pipelineCITraceCmd = &cobra.Command{
		Use:   "trace [<job-id>] [flags]",
		Short: `Trace a CI job log in real time`,
		Example: heredoc.Doc(`
	$ glab ci trace
	#=> interactively select a job to trace

	$ glab ci trace 224356863
	#=> trace job with id 224356863
	`),
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error

			// support `-R, --repo` override
			//
			// NOTE: it is important to assign the BaseRepo and HTTPClient in RunE because
			// they are overridden in a PersistentRun hook (when `-R, --repo` is specified)
			// which runs before RunE is executed
			opts.BaseRepo = f.BaseRepo
			opts.HTTPClient = f.HttpClient

			if len(args) != 0 {
				opts.JobID = utils.StringToInt(args[0])
			}
			if opts.Branch == "" {
				opts.Branch, err = git.CurrentBranch()
				if err != nil {
					return err
				}
			}
			if runE != nil {
				return runE(opts)
			}
			return TraceRun(opts)
		},
	}

	pipelineCITraceCmd.Flags().StringVarP(&opts.Branch, "branch", "b", "", "Check pipeline status for a branch. (Default is the current branch)")
	return pipelineCITraceCmd
}

func TraceRun(opts *TraceOpts) error {
	apiClient, err := opts.HTTPClient()
	if err != nil {
		return err
	}

	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	if opts.JobID < 1 {
		fmt.Fprintf(opts.IO.StdOut, "\nSearching for latest pipeline on %s...\n", opts.Branch)

		pipeline, err := api.GetLastPipeline(apiClient, repo.FullName(), opts.Branch)
		if err != nil {
			return err
		}

		fmt.Fprintf(opts.IO.StdOut, "Getting jobs for pipeline %d...\n\n", pipeline.ID)

		jobs, err := api.GetPipelineJobs(apiClient, pipeline.ID, repo.FullName())
		if err != nil {
			return err
		}

		var jobOptions []string
		var selectedJob string

		for _, job := range jobs {
			jobOptions = append(jobOptions, fmt.Sprintf("%s (%d) - %s", job.Name, job.ID, job.Status))
		}

		promptOpts := &survey.Select{
			Message: "Select pipeline job to trace:",
			Options: jobOptions,
		}

		err = prompt.AskOne(promptOpts, &selectedJob)
		if err != nil {
			if errors.Is(err, terminal.InterruptErr) {
				return nil
			}

			return err
		}

		if selectedJob != "" {
			re := regexp.MustCompile(`(?s)\((.*)\)`)
			m := re.FindAllStringSubmatch(selectedJob, -1)
			opts.JobID = utils.StringToInt(m[0][1])
		} else if len(jobs) > 0 {
			opts.JobID = jobs[0].ID
		} else {
			// use commit statuses to show external jobs
			cs, err := api.GetCommitStatuses(apiClient, repo.FullName(), pipeline.SHA)
			if err != nil {
				return nil
			}

			c := opts.IO.Color()

			fmt.Fprint(opts.IO.StdOut, "Getting external jobs...")
			for _, status := range cs {
				var s string

				switch status.Status {
				case "success":
					s = c.Green(status.Status)
				case "error":
					s = c.Red(status.Status)
				default:
					s = c.Gray(status.Status)
				}
				fmt.Fprintf(opts.IO.StdOut, "(%s) %s\nURL: %s\n\n", s, c.Bold(status.Name), c.Gray(status.TargetURL))
			}

			return nil
		}
	}

	job, err := api.GetPipelineJob(apiClient, opts.JobID, repo.FullName())
	if err != nil {
		return err
	}
	fmt.Fprintln(opts.IO.StdOut)

	err = ciutils.RunTrace(context.Background(), apiClient, opts.IO.StdOut, repo.FullName(), job, job.Name)
	if err != nil {
		return err
	}

	return nil
}
