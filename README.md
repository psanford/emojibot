# Emojibot

Slackbot that posts new emoji added to your workspace into a channel.

## Deployment

I deploy this as a lambda function behind an api gateway. You should also be able to run this as a standalone webserver.

Config is set via either environment variables or with ssm parameter store. If you use ssm parameter store set enviornment variable SSM_PATH to the prefix where these values will be set.


## App configuration in slack
To run create a new app on your workspace:
- Get Basic Information > App Credentials > Signing Secret. This is your SLACK_SIGNING_SECRET
- On OAuth & Permissions > Scopes > Bot Token Scopes add:
  - chat:write
  - emoji:read
- On OAuth & Permissions click 'Install to Workspace'
- Get OAuth & Permissions > OAuth Tokens for Your Workspace > Bot User OAuth Token. This is your SLACK_TOKEN
- On Event Subscriptions, enable events.
- On Event Subscriptions set Request URL to the url where this bot is running
- On Event Subscriptions > Subscribe to bot event > add 'emoji_changed' event.
