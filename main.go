package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/skratchdot/open-golang/open"
	flag "github.com/spf13/pflag"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// Retrieve a token, saves the token, then returns the generated client.
func createClient(config *oauth2.Config) *http.Client {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		log.Fatalf("Failed to find user cache dir: %v", err)
	}

	tokFile := filepath.Join(userCacheDir, "gmbackup", "token.json")
	tok, err := tokenFromFile(tokFile)
	if os.IsNotExist(err) {
		tok = tokenFromWeb(config)
		saveToken(tokFile, tok)
	} else if err != nil {
		log.Fatalf("Failed to load authorization token: %v", err)
	}

	return config.Client(context.Background(), tok)
}

// Request a token from the web, then returns the retrieved token.
func tokenFromWeb(config *oauth2.Config) *oauth2.Token {

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	config.RedirectURL = fmt.Sprintf("http://%v", listener.Addr().String())

	var tok2 *oauth2.Token
	var err2 error

	server := &http.Server{}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			err2 = fmt.Errorf("URL parameter 'code' is missing")
			io.WriteString(w, "Error: could not find 'code' URL parameter\n")
			go server.Close()
			return
		}

		tok, err := config.Exchange(context.TODO(), code)
		if err != nil {
			err2 = fmt.Errorf("unable to retrieve token: %v", err)
			io.WriteString(w, "Error: could not retrieve token\n")
			go server.Close()
			return
		}

		io.WriteString(w, "Login successful!\nYou can now close this window.\n")

		go server.Close()
		tok2 = tok
	})

	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)

	fmt.Printf("Go to the following link in your browser to authorize: \n%v\n", authURL)
	if err := open.Start(authURL); err != nil {
		log.Printf("Failed to open browser: %v", err)
	}

	err = server.Serve(listener)
	if err2 == nil && tok2 == nil {
		log.Fatalf("Error waiting for authorization callback: %v", err)
	}

	if err2 != nil {
		log.Fatalf("Authorization failed: %v", err2)
	}

	return tok2
}

// Retrieves a token from a local file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// Saves a token to a file path.
func saveToken(path string, token *oauth2.Token) {
	dir := filepath.Dir(path)
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		log.Fatalf("Unable to save oauth token: %v", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to save oauth token: %v", err)
	}
	defer f.Close()

	json.NewEncoder(f).Encode(token)
}

func readdir(path string) (map[string]os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	files, err := f.Readdir(0)
	if err != nil {
		return nil, err
	}

	fileMap := make(map[string]os.FileInfo)
	for _, v := range files {
		fileMap[v.Name()] = v
	}

	return fileMap, nil
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

	f, err := os.CreateTemp(outputPath, "gmbackup")
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(raw)
	if err != nil {
		return err
	}

	err = f.Chmod(0400)
	if err != nil {
		return err
	}

	err = os.Chtimes(f.Name(), time.Time{}, time.UnixMilli(msg.InternalDate))
	if err != nil {
		return err
	}

	path := filepath.Join(outputPath, id)
	err = os.Rename(f.Name(), path)
	if err != nil {
		return err
	}

	err = f.Close()
	if err != nil {
		return err
	}

	return nil
}

// This secret only identifies the application, it doesn't provide access to any
// user data.
var defaultConfig = oauth2.Config{
	ClientID:     "188301361501-76eocf83e1m0946ppeafa6rsu7ub60ss.apps.googleusercontent.com",
	ClientSecret: "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",
	Endpoint:     google.Endpoint,
	Scopes:       []string{gmail.GmailReadonlyScope},
}

func main() {
	delete := flag.BoolP("delete", "d", false, "Delete local mail that has been deleted in Gmail")
	dryRun := flag.BoolP("dry-run", "n", false, "Don't make any changes")
	user := flag.StringP("user", "u", "me", "Gmail account to backup")
	verbose := flag.BoolP("verbose", "v", false, "")
	incremental := flag.BoolP("incremental", "i", false, "Stop fetching on first existing mail, won't detect deletes")

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

	client := createClient(config)

	svc, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Gmail client: %v", err)
	}

	fileMap, err := readdir(outputPath)
	if err != nil {
		log.Fatalf("Unable to list messages: %v", err)
	}

	remoteMails := make(map[string]bool)
	var processed int64
	pageToken := ""
	for {
		req := svc.Users.Messages.List(*user).MaxResults(500).Q("-in:CHAT")
		if pageToken != "" {
			req.PageToken(pageToken)
		}
		r, err := req.Do()
		if err != nil {
			log.Fatalf("Unable to retrieve messages: %v", err)
		}

		for _, m := range r.Messages {
			remoteMails[m.Id] = true

			if fileMap[m.Id] == nil {
				if *verbose {
					log.Printf("Downloading %v", m.Id)
				}
				if !*dryRun {
					err = downloadMessage(svc, m.Id, *user, outputPath)
					if err != nil {
						log.Fatalf("Unable to retrieve message: %v: %v", m.Id, err)
					}
				}
			} else if *incremental {
				return
			}

			processed += 1
		}

		if r.NextPageToken == "" {
			break
		}
		pageToken = r.NextPageToken
	}

	if *delete {
		for k := range fileMap {
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
						log.Fatalf("Delete failed: %v", err)
					}
				}
			}
		}
	}
}
