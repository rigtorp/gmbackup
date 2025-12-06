// Copyright © 2023-2025 Erik Rigtorp <erik@rigtorp.se>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// A cross-platform helper to open a url in the default browser
func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}

// Creates a URL-safe, unpadded base64 random string
func generateRandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// OAuth2 specs require "Raw" (no padding) URL encoding
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Implements the S256 transformation
func generateCodeChallenge(verifier string) string {
	s := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(s[:])
}

// Retrieves a token from a local file.
func tokenFromFile(file string) (tok *oauth2.Token, err error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	tok = &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// Request a token from the web, then returns the retrieved token.
func tokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	ctx := context.Background()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %w", err)
	}

	config.RedirectURL = fmt.Sprintf("http://%v/callback", listener.Addr().String())

	// PKCE Generation (Verifier & Challenge)
	verifier, err := generateRandomString(32)
	if err != nil {
		return nil, fmt.Errorf("failed to generate verifier: %w", err)
	}
	challenge := generateCodeChallenge(verifier)
	state, err := generateRandomString(16)
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	codeChan := make(chan string)
	errChan := make(chan error)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "State mismatch", http.StatusBadRequest)
			errChan <- fmt.Errorf("state mismatch")
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Code not found", http.StatusBadRequest)
			errChan <- fmt.Errorf("code not found")
			return
		}

		if _, err := w.Write([]byte("Authentication successful! You can close this tab.")); err != nil {
			errChan <- fmt.Errorf("failed to write response: %w", err)
			return
		}

		codeChan <- code
	})

	server := &http.Server{
		Handler: mux,
	}

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	// We construct the Auth URL *after* we know the redirect URL
	authUrl := config.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.AccessTypeOffline,
	)

	log.Printf("Opening browser: %s\n", authUrl)
	if err := openBrowser(authUrl); err != nil {
		log.Printf("Failed to open browser: %v\n", err)
	}

	var authCode string
	select {
	case authCode = <-codeChan:
	case err := <-errChan:
		return nil, fmt.Errorf("callback error: %w", err)
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("timeout waiting for authentication")
	}

	if err := server.Shutdown(ctx); err != nil {
		return nil, fmt.Errorf("failed to shutdown server: %w", err)
	}

	token, err := config.Exchange(ctx, authCode, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}

	return token, nil
}

// Saves a token to a file path.
func saveToken(path string, token *oauth2.Token) error {
	dir := filepath.Dir(path)
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		return fmt.Errorf("unable to create directory for oauth token: %w", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("unable to open file for oauth token: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("Failed to close file: %v", err)
		}
	}()

	if err := json.NewEncoder(f).Encode(token); err != nil {
		return fmt.Errorf("failed to encode token: %w", err)
	}
	return nil
}

// Retrieve a token, saves the token, then returns the generated client.
func createClient(config *oauth2.Config) (*http.Client, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("failed to find user cache dir: %w", err)
	}

	tokFile := filepath.Join(userCacheDir, "gmbackup", "token.json")
	tok, err := tokenFromFile(tokFile)
	if os.IsNotExist(err) {
		tok, err = tokenFromWeb(config)
		if err != nil {
			return nil, err
		}
		if err := saveToken(tokFile, tok); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("failed to load authorization token: %w", err)
	}

	return config.Client(context.Background(), tok), nil
}

// Writes data to a file atomically.
func atomicWrite(filename string, data []byte, mtime time.Time) error {
	tempFile, err := os.CreateTemp(filepath.Dir(filename), "gmbackup-")
	if err != nil {
		return err
	}
	tempName := tempFile.Name()

	defer func() {
		// Only clean up if the file hasn't been successfully renamed yet.
		if err != nil {
			_ = tempFile.Close()
			_ = os.Remove(tempName)
		}
	}()

	if _, err = tempFile.Write(data); err != nil {
		return err
	}

	err = tempFile.Chmod(0400)
	if err != nil {
		return err
	}

	if err = tempFile.Close(); err != nil {
		return err
	}

	err = os.Chtimes(tempName, time.Time{}, mtime)
	if err != nil {
		return err
	}

	err = os.Rename(tempName, filename)
	return err
}

func downloadMessage(svc *gmail.Service, id string, user string, outputPath string) error {
	msg, err := svc.Users.Messages.Get(user, id).Format("raw").Do()
	if err != nil {
		return err
	}

	raw, err := base64.URLEncoding.DecodeString(msg.Raw)
	if err != nil {
		return err
	}

	path := filepath.Join(outputPath, id)

	err = atomicWrite(path, raw, time.UnixMilli(msg.InternalDate))
	if err != nil {
		return err
	}

	return nil
}

func readdir(path string) (fileMap map[string]os.FileInfo, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	files, err := f.Readdir(0)
	if err != nil {
		return nil, err
	}

	fileMap = make(map[string]os.FileInfo)
	for _, v := range files {
		fileMap[v.Name()] = v
	}

	return fileMap, nil
}

// This secret only identifies the application, it doesn't provide access to any
// user data.
var defaultConfig = oauth2.Config{
	ClientID:     "188301361501-76eocf83e1m0946ppeafa6rsu7ub60ss.apps.googleusercontent.com",
	ClientSecret: "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",
	Endpoint:     google.Endpoint,
	Scopes:       []string{gmail.GmailReadonlyScope},
}

var delete = flag.BoolP("delete", "d", false, "Delete local mail that has been deleted in Gmail")
var dryRun = flag.BoolP("dry-run", "n", false, "Don't make any changes")
var user = flag.StringP("user", "u", "me", "Gmail account to backup")
var verbose = flag.BoolP("verbose", "v", false, "")
var incremental = flag.BoolP("incremental", "i", false, "Stop fetching on first existing mail, won't detect deletes")

func sync(svc *gmail.Service, outputPath string) error {
	localMails, err := readdir(outputPath)
	if err != nil {
		return fmt.Errorf("unable to list local messages: %w", err)
	}

	remoteMails := make(map[string]bool)
	pageToken := ""
	for {
		req := svc.Users.Messages.List(*user).MaxResults(500)
		if pageToken != "" {
			req.PageToken(pageToken)
		}
		r, err := req.Do()
		if err != nil {
			return fmt.Errorf("unable to list remote messages: %w", err)
		}

		for _, m := range r.Messages {
			remoteMails[m.Id] = true

			if localMails[m.Id] == nil {
				if *verbose {
					log.Printf("Downloading %v", m.Id)
				}
				if !*dryRun {
					err = downloadMessage(svc, m.Id, *user, outputPath)
					if err != nil {
						return fmt.Errorf("unable to retrieve message: %s: %w", m.Id, err)
					}
				}
			} else if *incremental {
				return nil
			}
		}

		if r.NextPageToken == "" {
			break
		}
		pageToken = r.NextPageToken
	}

	if *delete {
		for k := range localMails {
			if !remoteMails[k] {
				if strings.HasPrefix(k, ".") {
					continue
				}
				if *verbose {
					log.Printf("Deleting %v", k)
				}
				if !*dryRun {
					path := filepath.Join(outputPath, k)
					err := os.Remove(path)
					if err != nil {
						return fmt.Errorf("delete failed: %w", err)
					}
				}
			}
		}
	}

	return nil
}

func main() {

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s: [DESTINATION]\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "Default destination is $HOME/mail/\n")
	}

	flag.Parse()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get user home dir: %v", err)
	}
	outputPath := filepath.Join(homeDir, "mail")

	if len(flag.Args()) > 1 {
		flag.Usage()
		os.Exit(1)
	}

	if len(flag.Args()) == 1 {
		outputPath = flag.Arg(0)
	}

	ctx := context.Background()
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatalf("U: %v", err)
	}

	credentialsPath := filepath.Join(userConfigDir, "gmbackup", "credentials.json")
	b, err := os.ReadFile(credentialsPath)
	config := &defaultConfig
	if err != nil {
		if *verbose {
			log.Printf("Unable to read client secret file: %v", err)
			log.Println("Using default client credentials")
		}
	} else {
		config, err = google.ConfigFromJSON(b, gmail.GmailReadonlyScope)
		if err != nil {
			log.Fatalf("Unable to parse client secret file to config: %v", err)
		}
		if *verbose {
			log.Printf("Using client credentials from: %v", credentialsPath)
		}
	}

	client, err := createClient(config)
	if err != nil {
		log.Fatalf("Unable to create client: %v", err)
	}

	svc, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Gmail client: %v", err)
	}

	err = sync(svc, outputPath)
	if err != nil {
		log.Fatalf("Unable to sync: %v", err)
	}
}
