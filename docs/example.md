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