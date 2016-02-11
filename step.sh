#!/bin/bash

this_script_dir="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

set -e

gemfile_path="$this_script_dir/Gemfile"
BUNDLE_GEMFILE="$gemfile_path" bundle install
BUNDLE_GEMFILE="$gemfile_path" bundle exec ruby "$this_script_dir/step.rb" \
                              -a "$service_account_email" \
                              -b "$package_name" \
                              -c "$apk_path" \
                              -d "$key_file_path" \
                              -e "$track" \
