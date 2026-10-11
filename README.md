# whatsapp2discord

A small HTTP service that receives [WhatsApp Cloud API](https://developers.facebook.com/docs/whatsapp/cloud-api/webhooks) webhooks and forwards incoming messages to a Discord channel through a [Discord webhook](https://support.discord.com/hc/en-us/articles/228383668).

No dependencies beyond the Go standard library.

## What it does

- Answers Meta's webhook verification handshake (`GET /webhook`).
- Checks the `X-Hub-Signature-256` HMAC on every `POST /webhook` and rejects anything unsigned.
- Posts each incoming message to Discord as `**Sender name (+number)**` followed by the message.
  Text, button/list replies, reactions, locations and shared contacts are rendered as text.
  Images, video, audio, stickers and documents are posted as a label plus caption; the media itself is not downloaded.
- Disables Discord mentions, so a WhatsApp sender can't ping `@everyone` or roles.
- Ignores delivery/read status updates.
- Returns `502` if Discord fails, so Meta redelivers later. Message IDs that were already forwarded are remembered in memory for 24 hours and skipped on redelivery. A restart clears that memory.
- Retries Discord rate limits (`429`) up to 3 times.

## Configuration

| Variable | Required | Description |
| --- | --- | --- |
| `WHATSAPP_VERIFY_TOKEN` | yes | Any string. Enter the same value as the "Verify token" in the Meta app's webhook settings. |
| `WHATSAPP_APP_SECRET` | yes | The Meta app secret (App settings → Basic). Used to verify webhook signatures. |
| `DISCORD_WEBHOOK_URL` | yes | Discord channel webhook URL (Channel settings → Integrations → Webhooks). |
| `MESSAGE_PREFIX` | no | Prepended verbatim to every outgoing Discord message. Unset or empty leaves messages unchanged. |
| `ADDR` | no | Listen address. Defaults to `:8080`. |

`GET /healthz` returns `ok`.

For example, set `MESSAGE_PREFIX="[WhatsApp] "` to label forwarded messages. Include any desired spaces or newlines in the value; no separator is added automatically. The prefix and message together are truncated to Discord's 2,000-character content limit. Mentions remain disabled, including in the prefix.

## Running

```sh
nix run github:tim-smart/whatsapp2discord
```

or build it:

```sh
nix build
./result/bin/whatsapp2discord
```

The service has to be reachable over HTTPS for Meta to deliver webhooks. Put it behind a reverse proxy, or use a tunnel such as `cloudflared` or `ngrok` while testing.

## Meta setup

1. Create an app at <https://developers.facebook.com/apps> and add the WhatsApp product.
2. Under WhatsApp → Configuration, set the callback URL to `https://<your-host>/webhook` and the verify token to `WHATSAPP_VERIFY_TOKEN`.
3. Subscribe to the `messages` webhook field.

## Development

```sh
nix develop
go test ./...
```
