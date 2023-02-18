# gmbackup

Simple tool to backup a Gmail account.

- Does a full sync using the [Gmail API](https://developers.google.com/gmail/api/guides).
- Saves only message contents, not metadata such as labels.
- Message saved in EML format, which just the raw email data in [RFC2822 format](https://www.rfc-editor.org/rfc/rfc2822).
- Restore functionality not yet implemented.

## Use your own client id

If the rate limits on the default client id becomes a problem you can create
your own client id in the [Google Cloud
Console](https://console.cloud.google.com/).

Drop the client credentials JSON file in
`$XDG_CONFIG_HOME/gmbackup/credentials.json`.
