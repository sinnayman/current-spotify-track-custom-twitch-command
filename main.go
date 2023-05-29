package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/gempir/go-twitch-irc"
	"github.com/gin-gonic/gin"
	"github.com/zmb3/spotify"
)

var (
	spotifyRedirectURI = "http://localhost:8080/callback"
)

type TwitchTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type TwitchChannel struct {
	ID     string `json:"id"`
	Handle string `json:"broadcaster_login"`
}

type SpotifyClient struct {
	client spotify.Client
	mu     sync.Mutex
}

var spotifyClient SpotifyClient

var (
	twitchClientID         = os.Getenv("TWITCH_CLIENT_ID")     // Get from env or just paste here
	twitchClientSecret     = os.Getenv("TWITCH_CLIENT_SECRET") // Replace with your Twitch Client Secret
	twitchRedirectURI      = os.Getenv("TWITCH_REDIRECT_URL")  // Replace with your Twitch Client Secret
	twitchBotUsername      = os.Getenv("TWITCH_BOT_USERNAME")
	twitchBroadcastChannel = os.Getenv("TWITCH_BROADCAST_CHANNEL")
	spotifyAuthCompleted   = false
	spotifyNeedsAuthHTML   = `<!DOCTYPE html>
<html>
	<head>
		<title>What's that playing?</title>
	</head>
	<body>
		<h3>Spotify not authenticated</h3>
		<p>You need to <a href='http://localhost:8080/spotify/login'>authenticate with spotify</a> before requesting this page .</p>
	</body>
</html>`
)

func main() {
	r := gin.Default()

	r.GET("/twitch/login", func(c *gin.Context) {
		if !spotifyAuthCompleted {
			c.HTML(200, "text/html", spotifyNeedsAuthHTML)
		}
		url := getTwitchAuthURL()
		c.Redirect(http.StatusTemporaryRedirect, url)
	})

	r.GET("/twitch/callback", func(c *gin.Context) {
		if !spotifyAuthCompleted {
			c.HTML(200, "text/html", spotifyNeedsAuthHTML)
		}

		code := c.Query("code")
		accessToken, err := getTwitchAccessToken(code)
		if err != nil {
			log.Fatal(err)
		}
		handleTwitchMessages(accessToken)
		c.String(http.StatusOK, "Successfully posted message to Twitch channel!")
	})

	// Spotify authorization flow
	r.GET("/spotify/login", func(c *gin.Context) {
		auth := spotifyAuth()
		url := auth.AuthURL("state")
		c.Redirect(http.StatusTemporaryRedirect, url)
	})

	r.GET("/spotify/callback", func(c *gin.Context) {
		auth := spotifyAuth()

		token, err := auth.Token("state", c.Request)
		if err != nil {
			log.Fatal(err)
		}

		spotifyClient.mu.Lock()
		defer spotifyClient.mu.Unlock()

		spotifyClient.client = auth.NewClient(token)
		if spotifyClient.client != (spotify.Client{}) {
			spotifyAuthCompleted = true
		}

		c.String(http.StatusOK, "Successfully authenticated with Spotify!")
	})

	r.GET("/spotify/current", func(c *gin.Context) {

		currentlyPlaying, err := getCurrentlyPlayingTrack()
		if err != nil {
			if currentlyPlaying.Playing {
				c.String(http.StatusOK, "Currently playing on Spotify: %s by %s", currentlyPlaying.Item.Name, currentlyPlaying.Item.Artists[0].Name)
			} else {
				c.String(http.StatusOK, "No track currently playing.")
			}
		}
	})

	r.Run(":8080")
}

func getCurrentlyPlayingTrack() (*spotify.CurrentlyPlaying, error) {
	spotifyClient.mu.Lock()
	defer spotifyClient.mu.Unlock()

	if spotifyClient.client == (spotify.Client{}) {
		return nil, errors.New("spotify auth fail")
	}

	currentlyPlaying, err := spotifyClient.client.PlayerCurrentlyPlaying()
	if err != nil {
		log.Fatal(err)
	}
	return currentlyPlaying, nil
}

func spotifyAuth() spotify.Authenticator {
	clientID := os.Getenv("SPOTIFY_CLIENT_ID")         // Get from env or just paste here
	clientSecret := os.Getenv("SPOTIFY_CLIENT_SECRET") // Get from env or just paste here
	redirectURL := os.Getenv("SPOTIFY_REDIRECT_URI")   // Get from env or just paste here

	if clientID == "" || clientSecret == "" {
		log.Fatal("Missing Spotify Client ID or Client Secret. Set the SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET environment variables.")
	}

	if redirectURL == "" {
		redirectURL = spotifyRedirectURI
	}

	auth := spotify.NewAuthenticator(redirectURL, spotify.ScopeUserReadCurrentlyPlaying)
	auth.SetAuthInfo(clientID, clientSecret)

	return auth
}

func getTwitchAuthURL() string {
	redirectURI := os.Getenv("TWITCH_REDIRECT_URI")
	if redirectURI == "" {
		redirectURI = twitchRedirectURI
	}

	scopes := []string{"chat:edit", "chat:read"}

	authURL := fmt.Sprintf("https://id.twitch.tv/oauth2/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=%s",
		twitchClientID, url.QueryEscape(redirectURI), strings.Join(scopes, " "))

	return authURL
}

func getTwitchAccessToken(code string) (string, error) {
	data := url.Values{}
	data.Set("client_id", twitchClientID)
	data.Set("client_secret", twitchClientSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", twitchRedirectURI)

	resp, err := http.PostForm("https://id.twitch.tv/oauth2/token", data)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var tokenResp TwitchTokenResponse
	err = json.Unmarshal(body, &tokenResp)
	if err != nil {
		return "", err
	}

	return tokenResp.AccessToken, nil
}

func handleTwitchMessages(oauthToken string) {
	token := "oauth:" + oauthToken
	client := twitch.NewClient(twitchBotUsername, token)
	client.OnNewMessage(func(channel string, user twitch.User, message twitch.Message) {
		if strings.HasPrefix(strings.ToLower(message.Text), "!sinnaybot song") {
			track, err := getCurrentlyPlayingTrack()
			if err == nil {
				message := ""
				if track.Item == nil {
					message = fmt.Sprintf("@%s, no song currently playing", user.Username)
				} else {
					message = fmt.Sprintf("@%s, the song currently playing is %s by %s", user.Username, track.Item.Name, track.Item.Artists[0].Name)
				}

				if message != "" {
					client.Say(channel, message)
				}

			}

		}
	})

	channel := twitchBroadcastChannel
	client.Join(channel)

	err := client.Connect()
	if err != nil {
		log.Fatalf("Failed to connect to twitch IRC: %s", err)
	}
}
