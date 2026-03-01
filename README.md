# gmbackup

Simple tool to backup a Gmail account.

- Performs full and incremental sync using the [Gmail API](https://developers.google.com/gmail/api/guides).
- Saves only message contents, not metadata such as labels.
- Message saved in EML format, which just the raw email data in [RFC2822 format](https://www.rfc-editor.org/rfc/rfc2822).
- Restore functionality not yet implemented.

## Installation

To install `gmbackup` using Go:

```bash
go install github.com/rigtorp/gmbackup@latest
```

Alternatively, you can build it from source:

```bash
git clone https://github.com/rigtorp/gmbackup.git
cd gmbackup
go build .
```

## Creating credentials.json

1. Go to the [Google Cloud Console](https://console.cloud.google.com/).
2. Create a new project or select an existing one.
3. Search for **Gmail API** and click **Enable**.
4. Go to **APIs & Services > OAuth consent screen**:
    - Select **External** (or **Internal** if using Google Workspace) and click **Create**.
    - Fill in the required fields (**App name**, **User support email**, **Developer contact information**).
    - Click **Save and Continue**.
    - Under **Scopes**, click **Add or Remove Scopes**, search for `https://www.googleapis.com/auth/gmail.readonly`, check it, and click **Add to table**. Click **Update** and then **Save and Continue**.
    - Under **Test users**, click **Add Users** and add your Gmail address. Click **Save and Continue**.
5. Go to **APIs & Services > Credentials**:
    - Click **Create Credentials > OAuth client ID**.
    - Select **Application type: Desktop app**.
    - Name it (e.g., `gmbackup`) and click **Create**.
    - Download the JSON file and rename it to `credentials.json`.

You can either provide the client credentials JSON file in
`$XDG_CONFIG_HOME/gmbackup/credentials.json` or use the `--client-id` and
`--client-secret` flags.
