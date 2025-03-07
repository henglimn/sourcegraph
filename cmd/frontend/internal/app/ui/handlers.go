package ui

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/gorilla/mux"
	"github.com/inconshreveable/log15"

	"github.com/sourcegraph/sourcegraph/cmd/frontend/auth"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/backend"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/envvar"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/globals"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/hubspot"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/hubspot/hubspotutil"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/app/jscontext"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/handlerutil"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/routevar"
	"github.com/sourcegraph/sourcegraph/internal/api"
	"github.com/sourcegraph/sourcegraph/internal/conf"
	"github.com/sourcegraph/sourcegraph/internal/cookie"
	"github.com/sourcegraph/sourcegraph/internal/env"
	"github.com/sourcegraph/sourcegraph/internal/errcode"
	"github.com/sourcegraph/sourcegraph/internal/gitserver"
	"github.com/sourcegraph/sourcegraph/internal/gitserver/gitdomain"
	"github.com/sourcegraph/sourcegraph/internal/repoupdater"
	"github.com/sourcegraph/sourcegraph/internal/search/result"
	"github.com/sourcegraph/sourcegraph/internal/search/symbol"
	"github.com/sourcegraph/sourcegraph/internal/types"
	"github.com/sourcegraph/sourcegraph/internal/vcs/git"
	"github.com/sourcegraph/sourcegraph/ui/assets"
)

type InjectedHTML struct {
	HeadTop    template.HTML
	HeadBottom template.HTML
	BodyTop    template.HTML
	BodyBottom template.HTML
}

type Metadata struct {
	// Title is the title of the page for Twitter cards, OpenGraph, etc.
	// e.g. "Open in Sourcegraph"
	Title string

	// Description is the description of the page for Twitter cards, OpenGraph,
	// etc. e.g. "View this link in Sourcegraph Editor."
	Description string

	// ShowPreview controls whether or not OpenGraph/Twitter card/etc metadata is rendered.
	ShowPreview bool

	// PreviewImage contains the URL of the preview image for relevant routes (e.g. blob).
	PreviewImage string
}

type Common struct {
	Injected InjectedHTML
	Metadata *Metadata
	Context  jscontext.JSContext
	Title    string
	Error    *pageError

	Manifest *assets.WebpackManifest

	WebpackDevServer bool // whether the Webpack dev server is running (WEBPACK_DEV_SERVER env var)

	// The fields below have zero values when not on a repo page.
	Repo         *types.Repo
	Rev          string // unresolved / user-specified revision (e.x.: "@master")
	api.CommitID        // resolved SHA1 revision
}

var webpackDevServer, _ = strconv.ParseBool(os.Getenv("WEBPACK_DEV_SERVER"))

// repoShortName trims the first path element of the given repo name if it has
// at least two path components.
func repoShortName(name api.RepoName) string {
	split := strings.Split(string(name), "/")
	if len(split) < 2 {
		return string(name)
	}
	return strings.Join(split[1:], "/")
}

// serveErrorHandler is a function signature used in newCommon and
// mockNewCommon. This is used as syntactic sugar to prevent programmer's
// (fragile creatures from planet Earth) from crashing out.
type serveErrorHandler func(w http.ResponseWriter, r *http.Request, err error, statusCode int)

// mockNewCommon is used in tests to mock newCommon (duh!).
//
// Ensure that the mock is reset at the end of every test by adding a call like the following:
//	defer func() {
//		mockNewCommon = nil
//	}()
var mockNewCommon func(w http.ResponseWriter, r *http.Request, title string, serveError serveErrorHandler) (*Common, error)

// newCommon builds a *Common data structure, returning an error if one occurs.
//
// In the event of the repository having been renamed, the request is handled
// by newCommon and nil, nil is returned. Basic usage looks like:
//
// 	common, err := newCommon(w, r, noIndex, serveError)
// 	if err != nil {
// 		return err
// 	}
// 	if common == nil {
// 		return nil // request was handled
// 	}
//
// In the case of a repository that is cloning, a Common data structure is
// returned but it has an incomplete RevSpec.
func newCommon(w http.ResponseWriter, r *http.Request, title string, indexed bool, serveError serveErrorHandler) (*Common, error) {
	if mockNewCommon != nil {
		return mockNewCommon(w, r, title, serveError)
	}

	manifest, err := assets.LoadWebpackManifest()
	if err != nil {
		return nil, errors.Wrap(err, "loading webpack manifest")
	}

	if !indexed {
		w.Header().Set("X-Robots-Tag", "noindex")
	}

	common := &Common{
		Injected: InjectedHTML{
			HeadTop:    template.HTML(conf.Get().HtmlHeadTop),
			HeadBottom: template.HTML(conf.Get().HtmlHeadBottom),
			BodyTop:    template.HTML(conf.Get().HtmlBodyTop),
			BodyBottom: template.HTML(conf.Get().HtmlBodyBottom),
		},
		Context:  jscontext.NewJSContextFromRequest(r),
		Title:    title,
		Manifest: manifest,
		Metadata: &Metadata{
			Title:       globals.Branding().BrandName,
			Description: "Sourcegraph is a web-based code search and navigation tool for dev teams. Search, navigate, and review code. Find answers.",
			ShowPreview: r.URL.Path == "/sign-in" && r.URL.RawQuery == "returnTo=%2F",
		},

		WebpackDevServer: webpackDevServer,
	}

	if _, ok := mux.Vars(r)["Repo"]; ok {
		// Common repo pages (blob, tree, etc).
		var err error
		common.Repo, common.CommitID, err = handlerutil.GetRepoAndRev(r.Context(), mux.Vars(r))
		isRepoEmptyError := routevar.ToRepoRev(mux.Vars(r)).Rev == "" && errors.HasType(err, &gitdomain.RevisionNotFoundError{}) // should reply with HTTP 200
		if err != nil && !isRepoEmptyError {
			var urlMovedError *handlerutil.URLMovedError
			if errors.As(err, &urlMovedError) {
				// The repository has been renamed, e.g. "github.com/docker/docker"
				// was renamed to "github.com/moby/moby" -> redirect the user now.
				err = handlerutil.RedirectToNewRepoName(w, r, urlMovedError.NewRepo)
				if err != nil {
					return nil, errors.Wrap(err, "when sending renamed repository redirect response")
				}

				return nil, nil
			}
			var repoSeeOtherError backend.ErrRepoSeeOther
			if errors.As(err, &repoSeeOtherError) {
				// Repo does not exist here, redirect to the recommended location.
				u, err := url.Parse(repoSeeOtherError.RedirectURL)
				if err != nil {
					return nil, err
				}
				u.Path, u.RawQuery = r.URL.Path, r.URL.RawQuery
				http.Redirect(w, r, u.String(), http.StatusSeeOther)
				return nil, nil
			}
			if errors.HasType(err, &gitdomain.RevisionNotFoundError{}) {
				// Revision does not exist.
				serveError(w, r, err, http.StatusNotFound)
				return nil, nil
			}
			if errors.HasType(err, &gitserver.RepoNotCloneableErr{}) {
				if errcode.IsNotFound(err) {
					// Repository is not found.
					serveError(w, r, err, http.StatusNotFound)
					return nil, nil
				}

				// Repository is not clonable.
				dangerouslyServeError(w, r, errors.New("repository could not be cloned"), http.StatusInternalServerError)
				return nil, nil
			}
			if gitdomain.IsRepoNotExist(err) {
				if gitdomain.IsCloneInProgress(err) {
					// Repo is cloning.
					return common, nil
				}
				// Repo does not exist.
				serveError(w, r, err, http.StatusNotFound)
				return nil, nil
			}
			if errcode.IsNotFound(err) || errcode.IsBlocked(err) {
				// Repo does not exist.
				serveError(w, r, err, http.StatusNotFound)
				return nil, nil
			}
			if errcode.IsUnauthorized(err) {
				// Not authorized to access repository.
				serveError(w, r, err, http.StatusUnauthorized)
				return nil, nil
			}
			return nil, err
		}
		if common.Repo.Name == "github.com/sourcegraphtest/Always500Test" {
			return nil, errors.New("error caused by Always500Test repo name")
		}
		common.Rev = mux.Vars(r)["Rev"]
		// Update gitserver contents for a repo whenever it is visited.
		go func() {
			ctx := context.Background()
			_, err = repoupdater.DefaultClient.EnqueueRepoUpdate(ctx, common.Repo.Name)
			if err != nil {
				log15.Error("EnqueueRepoUpdate", "error", err)
			}
		}()
	}

	// common.Repo and common.CommitID are populated in the above if statement
	if blobPath, ok := mux.Vars(r)["Path"]; ok && envvar.OpenGraphPreviewServiceURL() != "" && envvar.SourcegraphDotComMode() && common.Repo != nil {
		lineRange := findLineRangeInQueryParameters(r.URL.Query())

		var symbolResult *result.Symbol
		if lineRange != nil && lineRange.StartLine != 0 && lineRange.StartLineCharacter != 0 {
			// Do not slow down the page load if symbol data takes too long to retrieve.
			ctx, cancel := context.WithTimeout(r.Context(), time.Second*1)
			defer cancel()

			if symbolMatch, _ := symbol.GetMatchAtLineCharacter(
				ctx,
				types.RepoName{ID: common.Repo.ID, Name: common.Repo.Name},
				common.CommitID,
				strings.TrimLeft(blobPath, "/"),
				lineRange.StartLine-1,
				lineRange.StartLineCharacter-1,
			); symbolMatch != nil {
				symbolResult = &symbolMatch.Symbol
			}
		}

		common.Metadata.ShowPreview = true
		common.Metadata.PreviewImage = getBlobPreviewImageURL(envvar.OpenGraphPreviewServiceURL(), r.URL.Path, lineRange)
		common.Metadata.Description = fmt.Sprintf("%s/%s", globals.ExternalURL(), mux.Vars(r)["Repo"])
		common.Metadata.Title = getBlobPreviewTitle(blobPath, lineRange, symbolResult)
	}

	return common, nil
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

const (
	index   = true
	noIndex = false
)

func serveBrandedPageString(titles string, description *string, indexed bool) handlerFunc {
	return serveBasicPage(func(c *Common, r *http.Request) string {
		return brandNameSubtitle(titles)
	}, description, indexed)
}

func serveBasicPage(title func(c *Common, r *http.Request) string, description *string, indexed bool) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		common, err := newCommon(w, r, "", indexed, serveError)
		if err != nil {
			return err
		}
		if description != nil {
			common.Metadata.Description = *description
		}
		if common == nil {
			return nil // request was handled
		}
		common.Title = title(common, r)
		return renderTemplate(w, "app.html", common)
	}
}

func serveHome(w http.ResponseWriter, r *http.Request) error {
	common, err := newCommon(w, r, globals.Branding().BrandName, index, serveError)
	if err != nil {
		return err
	}
	if common == nil {
		return nil // request was handled
	}

	// Homepage redirects to /search.
	r.URL.Path = "/search"
	http.Redirect(w, r, r.URL.String(), http.StatusTemporaryRedirect)
	return nil
}

func serveSignIn(w http.ResponseWriter, r *http.Request) error {
	common, err := newCommon(w, r, "", index, serveError)
	if err != nil {
		return err
	}
	if common == nil {
		return nil // request was handled
	}
	common.Title = brandNameSubtitle("Sign in")

	return renderTemplate(w, "app.html", common)
}

// redirectTreeOrBlob redirects a blob page to a tree page if the file is actually a directory,
// or a tree page to a blob page if the directory is actually a file.
func redirectTreeOrBlob(routeName, path string, common *Common, w http.ResponseWriter, r *http.Request) (requestHandled bool, err error) {
	// NOTE: It makes no sense for this function to proceed if the commit ID
	// for the repository is empty. It is most likely the repository is still
	// clone in progress.
	if common.CommitID == "" {
		return false, nil
	}

	if path == "/" || path == "" {
		if routeName != routeRepo {
			// Redirect to repo route
			target := "/" + string(common.Repo.Name) + common.Rev
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return true, nil
		}
		return false, nil
	}
	stat, err := git.Stat(r.Context(), common.Repo.Name, common.CommitID, path)
	if err != nil {
		if os.IsNotExist(err) {
			serveError(w, r, err, http.StatusNotFound)
			return true, nil
		}
		return false, err
	}
	expectedDir := routeName == routeTree
	if stat.Mode().IsDir() != expectedDir {
		target := "/" + string(common.Repo.Name) + common.Rev + "/-/"
		if expectedDir {
			target += "blob"
		} else {
			target += "tree"
		}
		target += path
		http.Redirect(w, r, auth.SafeRedirectURL(target), http.StatusTemporaryRedirect)
		return true, nil
	}
	return false, nil
}

// serveTree serves the tree (directory) pages.
func serveTree(title func(c *Common, r *http.Request) string) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		common, err := newCommon(w, r, "", index, serveError)
		if err != nil {
			return err
		}
		if common == nil {
			return nil // request was handled
		}

		// File, directory, and repository pages with a revision ("@foobar") should not be indexed, only
		// the default revision should be indexed. Leading people to such pages through Google is harmful
		// as the person is often looking for a specific file/dir/repository and the indexed commit or
		// branch is outdated, leading to them getting the wrong result.
		if common.Rev != "" {
			w.Header().Set("X-Robots-Tag", "noindex")
		}

		handled, err := redirectTreeOrBlob(routeTree, mux.Vars(r)["Path"], common, w, r)
		if handled {
			return nil
		}
		if err != nil {
			return err
		}

		common.Title = title(common, r)
		return renderTemplate(w, "app.html", common)
	}
}

func serveRepoOrBlob(routeName string, title func(c *Common, r *http.Request) string) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		common, err := newCommon(w, r, "", index, serveError)
		if err != nil {
			return err
		}
		if common == nil {
			return nil // request was handled
		}

		// File, directory, and repository pages with a revision ("@foobar") should not be indexed, only
		// the default revision should be indexed. Leading people to such pages through Google is harmful
		// as the person is often looking for a specific file/dir/repository and the indexed commit or
		// branch is outdated, leading to them getting the wrong result.
		if common.Rev != "" {
			w.Header().Set("X-Robots-Tag", "noindex")
		}

		handled, err := redirectTreeOrBlob(routeName, mux.Vars(r)["Path"], common, w, r)
		if handled {
			return nil
		}
		if err != nil {
			return err
		}

		common.Title = title(common, r)

		q := r.URL.Query()
		_, isNewQueryUX := q["sq"] // sq URL param is only set by new query UX in SearchNavbarItem.tsx
		if search := q.Get("q"); search != "" && !isNewQueryUX {
			// Redirect old search URLs:
			//
			// 	/github.com/gorilla/mux@24fca303ac6da784b9e8269f724ddeb0b2eea5e7?q=ErrMethodMismatch&utm_source=chrome-extension
			// 	/github.com/gorilla/mux@24fca303ac6da784b9e8269f724ddeb0b2eea5e7/-/blob/mux.go?q=NewRouter
			//
			// To new ones:
			//
			// 	/search?q=repo:^github.com/gorilla/mux$+ErrMethodMismatch
			//
			// It does not apply the file: filter because that was not the behavior of the
			// old blob URLs with a 'q' parameter either.
			r.URL.Path = "/search"
			q.Set("sq", "repo:^"+regexp.QuoteMeta(string(common.Repo.Name))+"$")
			r.URL.RawQuery = q.Encode()
			http.Redirect(w, r, r.URL.String(), http.StatusPermanentRedirect)
			return nil
		}
		return renderTemplate(w, "app.html", common)
	}
}

// searchBadgeHandler serves the search readme badges from the search-badger service
// https://github.com/sourcegraph/search-badger
func searchBadgeHandler() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = "http"
			r.URL.Host = "search-badger"
			r.URL.Path = "/"
		},
		ErrorLog: log.New(env.DebugOut, "search-badger proxy: ", log.LstdFlags),
	}
}

func servePingFromSelfHosted(w http.ResponseWriter, r *http.Request) error {
	// CORS to allow request from anywhere
	u, err := url.Parse(r.Referer())
	if err != nil {
		return err
	}
	w.Header().Add("Access-Control-Allow-Origin", u.Host)
	w.Header().Add("Access-Control-Allow-Credentials", "true")
	if r.Method == http.MethodOptions {
		// CORS preflight request, respond 204 and allow origin header
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	email := r.URL.Query().Get("email")

	sourceURLCookie, err := r.Cookie("sourcegraphSourceUrl")
	var sourceURL string
	if err == nil && sourceURLCookie != nil {
		sourceURL = sourceURLCookie.Value
	}

	anonymousUserId, _ := cookie.AnonymousUID(r)

	hubspotutil.SyncUser(email, hubspotutil.SelfHostedSiteInitEventID, &hubspot.ContactProperties{
		IsServerAdmin:   true,
		AnonymousUserID: anonymousUserId,
		FirstSourceURL:  sourceURL,
	})
	return nil
}
