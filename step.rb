require 'optparse'
require 'tmpdir'
require 'open-uri'
require 'google/api_client'

# -----------------------
# --- Functions
# -----------------------

def log_fail(message)
  puts
  puts "\e[31m#{message}\e[0m"
  exit(1)
end

def log_warn(message)
  puts "\e[33m#{message}\e[0m"
end

def log_info(message)
  puts
  puts "\e[34m#{message}\e[0m"
end

def log_details(message)
  puts "  #{message}"
end

def log_done(message)
  puts "  \e[32m#{message}\e[0m"
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

  log_fail(message)
end

# -----------------------
# --- Main
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

#
# Print options
log_info('Configs:')
log_details('service_account_email: ***')
log_details("package_name: #{options[:package_name]}")
log_details("apk_path: #{options[:apk_path]}")
log_details('key_file_path: ***')

log_fail('service_account_email not specified') unless options[:service_account_email]
log_fail('package_name not specified') unless options[:package_name]
log_fail('track not specified') unless options[:track]

log_fail('apk_path not found') unless options[:apk_path] && File.exist?(options[:apk_path])
log_fail('key_file_path not provided') unless options[:key_file_path]

#
# Step
if options[:key_file_path].start_with?('http', 'https')
  log_details 'downloading key file...'
  tmp_dir = Dir.tmpdir
  tmp_key_file_path = File.join(tmp_dir, 'key_file.p12')

  begin
    download = open(options[:key_file_path])
    IO.copy_stream(download, tmp_key_file_path)
  rescue => ex
    log_fail "download failed with exception: #{ex}"
  end

  options[:key_file_path] = tmp_key_file_path.to_s
end

log_fail('key_file_path does not exist') unless File.exist?(options[:key_file_path])

client = Google::APIClient.new(
  application_name: 'Bitrise',
  application_version: '0.0.1'
)

# Authorization
log_info('Authorizing')
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
  log_fail("Failed to authorize user: #{ex}")
end

access_token = auth_client.fetch_access_token!
log_fail('Failed to authorize user: no access token get') unless access_token

# Publishing new version
log_info('Publishing new version')
android_publisher = client.discovered_api('androidpublisher', 'v2')

# Create a new edit
log_details('Create a new edit')
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
log_details('Upload apk')
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
log_details('Update track')
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
log_details('Commit edit')
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
