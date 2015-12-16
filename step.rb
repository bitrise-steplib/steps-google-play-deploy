require 'optparse'
require 'google/api_client'

# -----------------------
# --- functions
# -----------------------

def fail_with_message(message)
  puts "\e[31m#{message}\e[0m"
  exit(1)
end

def faile_with_message_and_delete_edit(message, client, publisher, edit, package_name, auth_client)
  unless edit.nil?
    result = client.execute(
      api_method: publisher.edits.delete,
      parameters: {
        'editId' => edit.data.id,
        'packageName' => package_name
      },
      authorization: auth_client
    )

    puts result.error_message if result.error?
  end

  fail_with_message(message)
end

# -----------------------
# --- main
# -----------------------

#
# Input validation
options = {
  service_account_email: nil,
  package_name: nil,
  apk_path: nil,
  key_file_path: nil,
  track: nil
}

parser = OptionParser.new do|opts|
  opts.banner = 'Usage: step.rb [options]'
  opts.on('-a', '--service_account email', 'Service Account Email') { |a| options[:service_account_email] = a unless a.to_s == '' }
  opts.on('-b', '--package name', 'Package Name') { |b| options[:package_name] = b unless b.to_s == '' }
  opts.on('-c', '--apk path', 'APK path') { |c| options[:apk_path] = c unless c.to_s == '' }
  opts.on('-d', '--key path', 'KEY path') { |d| options[:key_file_path] = d unless d.to_s == '' }
  opts.on('-e', '--track name', 'Track name') { |e| options[:track] = e unless e.to_s == '' }
  opts.on('-h', '--help', 'Displays Help') do
    exit
  end
end
parser.parse!

fail_with_message('service_account_email not specified') unless options[:service_account_email]
fail_with_message('package_name not specified') unless options[:package_name]
fail_with_message('track not specified') unless options[:track]
fail_with_message('apk_path not found') unless options[:apk_path] && File.exist?(options[:apk_path])
fail_with_message('key_file_path not found') unless options[:key_file_path] && File.exist?(options[:key_file_path])

#
# Print configs
puts
puts '========== Configs =========='
puts ' * service_account_email: ***'
puts " * package_name: #{options[:package_name]}"
puts " * track: #{options[:track]}"
puts " * apk_path: #{options[:apk_path]}"
puts ' * key_file_path: ***'

#
# Step
client = Google::APIClient.new(
  application_name: 'Bitrise',
  application_version: '0.0.1'
)

# Authorization
puts
puts '=> Authorizing'
key = Google::APIClient::KeyUtils.load_from_pkcs12(options[:key_file_path], 'notasecret')

auth_client = nil
begin
  auth_client = Signet::OAuth2::Client.new(
    token_credential_uri: 'https://accounts.google.com/o/oauth2/token',
    audience: 'https://accounts.google.com/o/oauth2/token',
    scope: 'https://www.googleapis.com/auth/androidpublisher',
    issuer: options[:service_account_email],
    signing_key: key
  )
rescue => ex
  fail_with_message("Failed to authorize user: #{ex}")
end

access_token = auth_client.fetch_access_token!
fail_with_message('Failed to authorize user: no access token get') unless access_token

# Publishing new version
puts
puts '=> Publishing new version'
android_publisher = client.discovered_api('androidpublisher', 'v2')

# Create a new edit
puts '  => Create a new edit'
edit = client.execute(
  api_method: android_publisher.edits.insert,
  parameters: { 'packageName' => options[:package_name] },
  authorization: auth_client
)
faile_with_message_and_delete_edit(
  edit.error_message,
  client,
  android_publisher,
  edit,
  options[:package_name],
  auth_client
) if edit.error?

# Upload apk
puts '  => Upload apk'
apk = Google::APIClient::UploadIO.new(File.expand_path(options[:apk_path]), 'application/vnd.android.package-archive')
result_upload = client.execute(
  api_method: android_publisher.edits.apks.upload,
  parameters: {
    'editId' => edit.data.id,
    'packageName' => options[:package_name],
    'uploadType' => 'media'
  },
  media: apk,
  authorization: auth_client
)
faile_with_message_and_delete_edit(
  result_upload.error_message,
  client,
  android_publisher,
  edit,
  options[:package_name],
  auth_client
) if result_upload.error?

# Update track
puts '  => Update track'
track_body = {
  'track' => options[:track],
  'userFraction' => 1.0,
  'versionCodes' => [result_upload.data.versionCode]
}

result_update = client.execute(
  api_method: android_publisher.edits.tracks.update,
  parameters: {
    'editId' => edit.data.id,
    'packageName' => options[:package_name],
    'track' => options[:track]
  },
  body_object: track_body,
  authorization: auth_client
)
faile_with_message_and_delete_edit(
  result_update.error_message,
  client,
  android_publisher,
  edit,
  options[:package_name],
  auth_client
) if result_update.error?

# Commit edit
puts '  => Commit edit'
result = client.execute(
  api_method: android_publisher.edits.commit,
  parameters: {
    'editId' => edit.data.id,
    'packageName' => options[:package_name]
  },
  authorization: auth_client
)
faile_with_message_and_delete_edit(
  result.error_message,
  client,
  android_publisher,
  edit,
  options[:package_name],
  auth_client
) if result.error?
