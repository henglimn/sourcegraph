package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/inconshreveable/log15"

	"github.com/sourcegraph/sourcegraph/internal/actor"
	"github.com/sourcegraph/sourcegraph/internal/authz"
	"github.com/sourcegraph/sourcegraph/internal/cookie"
	"github.com/sourcegraph/sourcegraph/internal/database"
	"github.com/sourcegraph/sourcegraph/internal/database/dbutil"
)

func serveVerifyEmail(db dbutil.DB) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		email := r.URL.Query().Get("email")
		verifyCode := r.URL.Query().Get("code")
		actr := actor.FromContext(ctx)
		if !actr.IsAuthenticated() {
			redirectTo := r.URL.String()
			q := make(url.Values)
			q.Set("returnTo", redirectTo)
			http.Redirect(w, r, "/sign-in?"+q.Encode(), http.StatusFound)
			return
		}
		// 🚨 SECURITY: require correct authed user to verify email
		usr, err := database.Users(db).GetByCurrentAuthUser(ctx)
		if err != nil {
			httpLogAndError(w, "Could not get current user", http.StatusUnauthorized)
			return
		}
		email, alreadyVerified, err := database.UserEmails(db).Get(ctx, usr.ID, email)
		if err != nil {
			http.Error(w, fmt.Sprintf("No email %q found for user %d", email, usr.ID), http.StatusBadRequest)
			return
		}
		if alreadyVerified {
			http.Error(w, fmt.Sprintf("User %d email %q is already verified", usr.ID, email), http.StatusBadRequest)
			return
		}
		verified, err := database.UserEmails(db).Verify(ctx, usr.ID, email, verifyCode)
		if err != nil {
			httpLogAndError(w, "Could not verify user email", http.StatusInternalServerError, "userID", usr.ID, "email", email, "error", err)
			return
		}
		if !verified {
			http.Error(w, "Could not verify user email. Email verification code did not match.", http.StatusUnauthorized)
			return
		}
		// Set the verified email as primary if user has no primary email
		_, _, err = database.UserEmails(db).GetPrimaryEmail(ctx, usr.ID)
		if err != nil {
			if err := database.UserEmails(db).SetPrimaryEmail(ctx, usr.ID, email); err != nil {
				httpLogAndError(w, "Could not set primary email.", http.StatusInternalServerError, "userID", usr.ID, "email", email, "error", err)
				return
			}
		}

		logEmailVerified(ctx, db, r, actr.UID)

		if err = database.Authz(db).GrantPendingPermissions(ctx, &database.GrantPendingPermissionsArgs{
			UserID: usr.ID,
			Perm:   authz.Read,
			Type:   authz.PermRepos,
		}); err != nil {
			log15.Error("Failed to grant user pending permissions", "userID", usr.ID, "error", err)
		}

		http.Redirect(w, r, "/user/settings/emails", http.StatusFound)
	}
}

func logEmailVerified(ctx context.Context, db dbutil.DB, r *http.Request, userID int32) {
	event := &database.SecurityEvent{
		Name:      database.SecurityEventNameEmailVerified,
		URL:       r.URL.Path,
		UserID:    uint32(userID),
		Argument:  nil,
		Source:    "BACKEND",
		Timestamp: time.Now(),
	}
	event.AnonymousUserID, _ = cookie.AnonymousUID(r)

	database.SecurityEventLogs(db).LogEvent(ctx, event)
}

func httpLogAndError(w http.ResponseWriter, msg string, code int, errArgs ...interface{}) {
	log15.Error(msg, errArgs...)
	http.Error(w, msg, code)
}
