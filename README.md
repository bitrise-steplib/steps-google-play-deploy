# Google Play Deploy

[![Step changelog](https://shields.io/github/v/release/bitrise-io/steps-google-play-deploy?include_prereleases&label=changelog&color=blueviolet)](https://github.com/bitrise-io/steps-google-play-deploy/releases)

Upload your Android app to Google Play.

<details>
<summary>Description</summary>

The Step uploads your Android app to Google Play. It works with both APK and AAB files.

Please note that in order to successfully use this Step, you must [upload your first APK or AAB file manually](https://support.google.com/googleplay/android-developer/answer/9859152?hl=en&visit_id=637407764704794872-3953166533&rd=1), using Google's own web interface! 
Once you uploaded one APK or AAB of your app to Google Play manually, you can use our Step for all subsequent versions. 

### Configuring the Step

The Step uses Google's API so before attempting to use the Step, you need to [Set up Google API access](https://devcenter.bitrise.io/deploy/android-deploy/deploying-android-apps/#setting-up-google-play-api-access). This includes:
- [Linking your Google Developer Console to an API project](https://developers.google.com/android-publisher/getting_started#linking_your_api_project).
- [Setting up API access using a service account](https://developers.google.com/android-publisher/getting_started#using_a_service_account).
- Granting the necessary access rights to the service account. 
- Upload the service account JSON key to Bitrise and store it in a [Secret Env Var](https://devcenter.bitrise.io/builds/env-vars-secret-env-vars/). 

Due to the way the Google Play Publisher API works, you have to grant at least the following permissions to that service account:
- Edit store listing, pricing & distribution
- Manage Production APKs
- Manage Alpha & Beta APKs
- Manage Alpha & Beta users

Read the full process in our [Deploying Android apps guide](https://devcenter.bitrise.io/deploy/android-deploy/deploying-android-apps/).

To deploy your app with the Step:

1. In the **Service Account JSON key file path**, add the Secret that stores your service account JSON key. 
1. In the **App file path** input, set the path to your APK and/or AAB files. You can add multiple paths here, separated with a newline. 
   In most cases, the default values work well unless you changed the output variable of the Step that build your APK or AAB.
1. In the **Package name**  input, set the package name of your app.  
1. In the **Track** input, add the track to which you want to assign the app. This can be any of the built-in tracks or a custom track of your own.

### Troubleshooting 

If the Step fails, check the following:
- If it's an authentication error, check that your Secret points to the correct file (and that a file is uploaded at all). 
- Make sure your service account has the necessary access rights.
- Check that there's no typo in the package name and that you selected an existing track for the app. 

### Useful links 

- [Google Play Developer API - Getting Started](https://developers.google.com/android-publisher/getting_started)
- [Deploying Android apps](https://devcenter.bitrise.io/deploy/android-deploy/deploying-android-apps/)

### Related Steps 

- [TestFairy Deploy Android](https://www.bitrise.io/integrations/steps/testfairy-deploy-android)
- [AppCenter Android Deploy](https://www.bitrise.io/integrations/steps/appcenter-deploy-android)
- [Appetize.io Deploy](https://www.bitrise.io/integrations/steps/appetize-deploy)
- [Android Sign](https://www.bitrise.io/integrations/steps/sign-apk)
</details>

## 🧩 Get started

Add this step directly to your workflow in the [Bitrise Workflow Editor](https://devcenter.bitrise.io/steps-and-workflows/steps-and-workflows-index/).

You can also run this step directly with [Bitrise CLI](https://github.com/bitrise-io/bitrise).

### Example

Build, sign and deploy your app to Google Play:

```yaml
steps:
- android-build:
    inputs:
    - variant: release
    - build_type: aab
- sign-apk:
    inputs:
    - android_app: $BITRISE_AAB_PATH
    # Make sure that the keystore file is uploaded in Code Signing settings
- google-play-deploy:
    inputs:
    - service_account_json_key_path: $SERVICE_ACCOUNT_KEY_URL # Upload this in Code Signing settings
    - package_name: my.example.package
    - app_path: $BITRISE_SIGNED_AAB_PATH
    - track: alpha
```

## ⚙️ Configuration

<details>
<summary>Inputs</summary>

| Key | Description | Flags | Default |
| --- | --- | --- | --- |
| `service_account_json_key_path` | Path to the service account's JSON key file. It must be a Secret Environment Variable, pointing to either a file uploaded to Bitrise or to a remote download location. | required, sensitive |  |
| `package_name` | Package name of the app. | required |  |
| `app_path` | Path to the app bundle file(s) or APK file(s) to deploy. In the case of [multiple artifacts](https://developer.android.com/google/play/publishing/multiple-apks.html) deploy, you can specify multiple APKs and AABs as a newline (`\n`) or pipe (`\|`) separated list. | required | `$BITRISE_APK_PATH\n$BITRISE_AAB_PATH` |
| `expansionfile_path` | Path to the [expansion file](https://developer.android.com/google/play/expansion-files). Leave empty or provide exactly the same number of paths as in app_path, separated by `\|` character and start each path with the expansion file's type separated by a `:`. (main, patch) Format examples: - `main:/path/to/my/app.obb` - `patch:/path/to/my/app1.obb\|main:/path/to/my/app2.obb\|main:/path/to/my/app3.obb` |  |  |
| `track` | The track to which you want to assign the uploaded app.  Can be one of the built-in tracks (internal, alpha, beta, production), or a custom track name you added in Google Play Developer Console. | required | `alpha` |
| `user_fraction` | Portion of the users who should get the staged version of the app. Accepts values between 0.0 and 1.0 (exclusive-exclusive). Only applies if `Status` is `inProgress` or `halted`.  To release to all users, this input should not be defined (or should be blank). |  |  |
| `status` | The status of a release. For more information see the [API reference](https://developers.google.com/android-publisher/api-ref/rest/v3/edits.tracks#Status). |  |  |
| `release_name` | The name of the release. By default Play Store generates the name from the APK's `versionName` value. |  |  |
| `update_priority` | This allows your app to decide how strongly to recommend an update to the user. Accepts values between 0 and 5 with 0 being the lowest priority and 5 being the highest priority. By default this value is 0. For more information see here: https://developer.android.com/guide/playcore/in-app-updates#check-priority. |  | `0` |
| `whatsnews_dir` | Use this input to specify localized 'what's new' files directory. This directory should contain 'whatsnew' files postfixed with the locale. what's new file name pattern: `whatsnew-LOCALE` Example:  ``` + - [PATH/TO/WHATSNEW]     \|     + - whatsnew-en-US     \|     + - whatsnew-de-DE ``` Format examples: - "./"         # what's new files are in the repo root directory - "./whatsnew" # what's new files are in the whatsnew directory |  |  |
| `mapping_file` | The `mapping.txt` file provides a translation between the original and obfuscated class, method, and field names. Uploading a mapping file is not required when deploying an AAB as the app bundle contains the mapping file itself. In case of deploying [multiple artifacts](https://developer.android.com/google/play/publishing/multiple-apks.html), you can specify multiple mapping.txt files as a newline (`\n`) or pipe (`\|`) separated list. The order of mapping files should match the list of APK or AAB files in the `app_path` input. |  | `$BITRISE_MAPPING_PATH` |
| `retry_without_sending_to_review` | If set to `true` and the initial change request fails, the changes will not be reviewed until they are manually sent for review from the Google Play Console UI. If set to `false`, the step fails if the changes can't be automatically sent to review. | required | `false` |
| `ack_bundle_installation_warning` | Must be set to `true` if the App Bundle installation may trigger a warning on user devices (for example, if installation size may be over a threshold, typically 100 MB). | required | `false` |
</details>

<details>
<summary>Outputs</summary>

| Key              | Description                                        |                                      
|------------------|----------------------------------------------------|
| `FAILURE_REASON` | Reason the upload to Google Play failed, if it did |

</details>

## 🙋 Contributing

We welcome [pull requests](https://github.com/bitrise-io/steps-google-play-deploy/pulls) and [issues](https://github.com/bitrise-io/steps-google-play-deploy/issues) against this repository.

For pull requests, work on your changes in a forked repository and use the Bitrise CLI to [run step tests locally](https://devcenter.bitrise.io/bitrise-cli/run-your-first-build/).

**Note:** this step's end-to-end tests (defined in `e2e/bitrise.yml`) are working with secrets which are intentionally not stored in this repo. External contributors won't be able to run those tests. Don't worry, if you open a PR with your contribution, we will help with running tests and make sure that they pass.

Learn more about developing steps:

- [Create your own step](https://devcenter.bitrise.io/contributors/create-your-own-step/)
- [Testing your Step](https://devcenter.bitrise.io/contributors/testing-and-versioning-your-steps/)
