package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/google/go-github/v47/github"
	"github.com/google/subcommands"
	"golang.org/x/oauth2"
)

const (
	GithubTokenEnv = "GH_TOKEN"
)

type syncGHCmd struct {
	archived bool
	dryRun   bool
	prune    bool
	worktree bool
	users    []string
	orgs     []string
}

func (c syncGHCmd) Name() string { return "syncgh" }
func (c syncGHCmd) Synopsis() string {
	return "sync list of checked out repositories with a github user/org"
}

func (c syncGHCmd) Usage() string {
	return `repos syncgh [-archived] [-dryrun] [-prune] [-worktree] [-user=XXX]... [-org=XXX]...

Authentication uses the GH_TOKEN environent variable.
`
}

func (c *syncGHCmd) SetFlags(fset *flag.FlagSet) {
	fset.BoolVar(&c.archived, "archived", false, "include archived repositories")
	fset.BoolVar(&c.dryRun, "dryrun", false, "print actions instead of executing them")
	fset.BoolVar(&c.prune, "prune", false, "prune repositories not found on the remote")
	fset.BoolVar(&c.worktree, "worktree", false, "nest checkouts under repo/default")
	fset.Func("user", "github user", func(s string) error {
		c.users = append(c.users, s)
		return nil
	})
	fset.Func("org", "github org", func(s string) error {
		c.orgs = append(c.orgs, s)
		return nil
	})
}

func (c syncGHCmd) Execute(ctx context.Context, fset *flag.FlagSet, args ...any) subcommands.ExitStatus {
	if fset.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "repos syncgh: unexpected args:", args)
		return subcommands.ExitUsageError
	}

	err := c.run(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repos syncgh:", err)
		return subcommands.ExitFailure
	}
	return subcommands.ExitSuccess
}

func (c syncGHCmd) run(ctx context.Context) error {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv(GithubTokenEnv)},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	allReposM := make(map[string]string)
	for _, user := range c.users {
		for page := 1; true; page++ {
			repos, res, err := client.Repositories.List(ctx, user, &github.RepositoryListOptions{
				ListOptions: github.ListOptions{
					Page:    page,
					PerPage: 100,
				},
			})
			if err != nil {
				return fmt.Errorf("list repos page %d for %s: %v", page, user, err)
			}
			for _, repo := range repos {
				if !c.archived && *repo.Archived {
					continue
				}
				allReposM[*repo.Name] = *repo.Owner.Login
			}
			if page >= res.LastPage {
				break
			}
		}
	}
	for _, org := range c.orgs {
		for page := 1; true; page++ {
			repos, res, err := client.Repositories.ListByOrg(ctx, org, &github.RepositoryListByOrgOptions{
				ListOptions: github.ListOptions{
					Page:    page,
					PerPage: 100,
				},
			})
			if err != nil {
				return fmt.Errorf("list repos page %d for %s: %v", page, org, err)
			}
			for _, repo := range repos {
				if !c.archived && *repo.Archived {
					continue
				}
				allReposM[*repo.Name] = *repo.Owner.Login
			}
			if page >= res.LastPage {
				break
			}
		}
	}

	localRepoM := make(map[string]struct{})
	des, err := os.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read .: %w", err)
	}
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		localRepoM[de.Name()] = struct{}{}
	}

	var toClone []struct {
		owner, repo string
	}
	for k, v := range allReposM {
		if _, ok := localRepoM[k]; !ok {
			toClone = append(toClone, struct {
				owner string
				repo  string
			}{
				v, k,
			})
		}
	}
	sort.Slice(toClone, func(i, j int) bool {
		if toClone[i].owner != toClone[j].owner {
			return toClone[i].owner < toClone[j].owner
		}
		return toClone[i].repo < toClone[j].repo
	})
	var toPrune []string
	for r := range localRepoM {
		if _, ok := allReposM[r]; !ok {
			toPrune = append(toPrune, r)
		}
	}
	sort.Strings(toPrune)

	for _, r := range toClone {
		u := fmt.Sprintf("https://github.com/%s/%s", r.owner, r.repo)
		dst := r.repo
		if c.worktree {
			dst += "/default"
		}
		msg := "git clone " + u + " " + dst
		if !c.dryRun {
			cmd := exec.Command("git", "clone", u, dst)
			out, err := cmd.CombinedOutput()
			if err != nil {
				msg += ": " + err.Error() + "\n" + string(out)
			}
		}
		fmt.Fprintln(os.Stderr, msg)
	}
	for _, r := range toPrune {
		msg := "rm -rf " + r
		if !c.dryRun {
			err := os.RemoveAll(r)
			if err != nil {
				msg += ": " + err.Error()
			}
		}
		fmt.Fprintln(os.Stderr, msg)
	}
	return nil
}
