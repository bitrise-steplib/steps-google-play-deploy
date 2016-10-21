package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/bitrise-io/go-utils/fileutil"
)

func main() {
	jsonKeyPth := "/Users/godrei/Downloads/key.json"
	packageName := "com.godrei.multiplatform"
	apkPath := "/Users/godrei/Downloads/com.godrei.multiplatform.apk"

	//
	// Create client
	jsonKeyBytes, err := fileutil.ReadBytesFromFile(jsonKeyPth)
	if err != nil {
		log.Fatalf("Failed to read json key, error: %s", err)
		os.Exit(1)
	}

	config, err := google.JWTConfigFromJSON(jsonKeyBytes, "https://www.googleapis.com/auth/androidpublisher")
	if err != nil {
		log.Fatalf("Failed to create config, error: %s", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("config.Email: %s\n", config.Email)
	// fmt.Printf("config.PrivateKey: %s\n", config.PrivateKey)
	fmt.Printf("config.Scopes: %s\n", config.Scopes)
	fmt.Printf("config.TokenURL: %s\n", config.TokenURL)

	client := config.Client(oauth2.NoContext)
	// ---

	//
	// Create insert edit
	insertEditURL := fmt.Sprintf("https://www.googleapis.com/androidpublisher/v2/applications/%s/edits", packageName)
	request, err := http.NewRequest("POST", insertEditURL, nil)
	if err != nil {
		log.Fatalf("Failed to create insert edit request, error: %s", err)
		os.Exit(1)
	}

	response, err := client.Do(request)
	if err != nil {
		log.Fatalf("Failed to perform request, error: %s", err)
		os.Exit(1)
	}
	defer response.Body.Close()

	fmt.Println()
	fmt.Printf("response status: %d\n", response.StatusCode)

	if response.StatusCode != 200 {
		log.Fatalf("Non success status code: %d", response.StatusCode)
		os.Exit(1)
	}

	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		log.Fatalf("Failed to read response, error: %s", err)
		os.Exit(1)
	}

	var respJSON map[string]interface{}
	if err := json.Unmarshal(body, &respJSON); err != nil {
		log.Fatalf("Failed to marshal response, error: %s", err)
		os.Exit(1)
	}

	editID := respJSON["id"]

	fmt.Println()
	fmt.Printf("editID: %s\n", editID)
	// ---

	//
	// Upload APK
	fileUploadURL := fmt.Sprintf("https://www.googleapis.com/upload/androidpublisher/v2/applications/%s/edits/%s/apks?uploadType=media", packageName, editID)
	apkFile, err := os.Open(apkPath)
	if err != nil {
		log.Fatalf("Failed to read apk (%s), error: %s", apkPath, err)
		os.Exit(1)
	}

	request, err = http.NewRequest("POST", fileUploadURL, apkFile)
	if err != nil {
		log.Fatalf("Failed to create upload request, error: %s", err)
		os.Exit(1)
	}
	request.Header.Set("content-type", "application/vnd.android.package-archive")

	response, err = client.Do(request)
	if err != nil {
		log.Fatalf("Failed to perform request, error: %s", err)
		os.Exit(1)
	}
	defer response.Body.Close()

	fmt.Println()
	fmt.Printf("response status: %d\n", response.StatusCode)

	if response.StatusCode != 200 {
		log.Fatalf("Non success status code: %d", response.StatusCode)
		os.Exit(1)
	}
	body, err = ioutil.ReadAll(response.Body)
	if err != nil {
		log.Fatalf("Failed to read response, error: %s", err)
		os.Exit(1)
	}
	fmt.Printf("%s\n", string(body))
	// ---
}
