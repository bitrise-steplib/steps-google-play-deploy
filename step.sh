#!/bin/bash

this_script_dir="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

set -e

current_dir=$(pwd)
cd $this_script_dir
bundle install
bundle exec ruby "step.rb" \
 -a $service_account_email \
 -b $package_name \
 -c $apk_file_path \
 -d $key_file_path \
 -e $track \
cd $current_dir
