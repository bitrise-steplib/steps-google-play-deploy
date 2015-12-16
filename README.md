# Google Play Deploy Step

## How to use this Step

Can be run directly with the [bitrise CLI](https://github.com/bitrise-io/bitrise),
just `git clone` this repository, `cd` into it's folder in your Terminal/Command Line
and call `bitrise run test`.

*Check the `bitrise.yml` file for required inputs which have to be
added to your `.bitrise.secrets.yml` file!*

## Run the tests in Docker, with `docker-compose`

You can call `docker-compose run --rm app bitrise run test` to run the test
inside the Bitrise Android Docker image.
