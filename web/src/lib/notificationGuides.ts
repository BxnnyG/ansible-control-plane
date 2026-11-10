import type { ChannelType } from './notifications';

/** In-UI setup guide of one channel type. */
export interface Guide {
  title: string;
  intro: string;
  steps: string[];
  notes: string[];
  /** Optional shell snippet shown below the steps. */
  example?: { caption: string; code: string };
}

const EGRESS_NOTE =
  'The application container itself sends the message, so it must be allowed to reach the target (firewall / egress allowlist). A failing test with "request failed" or a timeout usually means the target is not reachable from the container.';

export const GUIDES: Record<ChannelType, Guide> = {
  ntfy: {
    title: 'ntfy setup',
    intro: 'ntfy delivers push notifications to your phone or browser through a simple topic.',
    steps: [
      'Choose a server: the public https://ntfy.sh needs no account, or enter the URL of your self-hosted ntfy server.',
      'Pick an unguessable topic name (for example a random string of 16+ characters). On a public server anyone who knows the topic can read and post to it.',
      'Optional: if the server requires authentication (self-hosted with access control, or a reserved topic), choose how to log in. Access token: create a token (it starts with tk_) in the ntfy web app under Account and enter it as the credential. Username and password: choose basic authentication, enter the ntfy username and use the account password as credential.',
      'Install the ntfy app (or open the web app) and subscribe to the same topic on the same server.',
      'Optional priority 1-5 (1 min, 3 default, 5 max/urgent). Leave empty for the default.',
      'Use "Send test" to verify. The message should arrive in your subscription within seconds.',
    ],
    notes: [EGRESS_NOTE],
  },
  matrix: {
    title: 'Matrix setup',
    intro: 'Messages are posted into one room by a bot account using its access token.',
    steps: [
      'Create a dedicated bot user on your homeserver (do not reuse your personal account).',
      'Obtain an access token for the bot. Either log in with Element as the bot and copy it from Settings, Help & About, Advanced, Access Token, or call the login API: POST /_matrix/client/v3/login with {"type":"m.login.password","identifier":{"type":"m.id.user","user":"<bot>"},"password":"<password>"} and use the returned access_token. Do not log out of that session afterwards, that would invalidate the token.',
      'Create the target room (or pick an existing one) and invite the bot. Accept the invite as the bot (join the room); a bot that has only been invited cannot post.',
      'Find the internal room ID: in Element open Room settings, Advanced, "Internal room ID". It looks like !abc123:example.org (not the #alias).',
      'Enter the homeserver URL (for example https://matrix.example.org), the room ID and the access token as credential, then use "Send test".',
    ],
    notes: [
      'Encrypted rooms are NOT supported: the sender posts plain m.room.message events and does no end-to-end encryption. In an encrypted room the message is sent unencrypted and clients show a warning or may hide it. Use an unencrypted room for notifications.',
      EGRESS_NOTE,
    ],
  },
  smtp: {
    title: 'E-mail (SMTP) setup',
    intro: 'Messages are sent through an SMTP server you provide.',
    steps: [
      'Enter the SMTP host and choose the security mode: STARTTLS (usually port 587), TLS / implicit TLS (usually port 465), or None (port 25, plaintext, requires the explicit "insecure" confirmation).',
      'If the server requires authentication, enter the username and the password as credential. For providers such as Gmail or Outlook with two-factor authentication, create an app password and use that instead of your account password.',
      'The From address must be allowed by the server (many providers only accept their own domain or a verified sender). Rejected senders show up as a failed test.',
      'Enter one or more recipients, separated by commas.',
      'Use "Send test". Check the spam folder if nothing arrives.',
    ],
    notes: [
      'Port is optional: empty means the default of the chosen security mode (587 for STARTTLS, 465 for TLS, 25 for none).',
      'If the server uses a private CA, paste its certificate under "Custom CA certificate".',
      EGRESS_NOTE + ' Many hosting providers block outbound port 25.',
    ],
  },
  gotify: {
    title: 'Gotify setup',
    intro: 'Gotify is a self-hosted push server; messages are posted with an application token.',
    steps: [
      'In the Gotify web UI open Apps and create a new application (for example "Control Plane").',
      'Copy the token of that application (not a client token) and enter it as the credential.',
      'Enter the base URL of your Gotify server, for example https://gotify.example.org (without /message).',
      'Optional priority (integer, 0-10 depending on your client settings). Empty uses 5.',
      'Use "Send test" and check the Gotify app or web UI.',
    ],
    notes: [EGRESS_NOTE],
  },
  webhook: {
    title: 'Webhook setup',
    intro: 'Each event is sent as an HTTP POST with a JSON body to your URL.',
    steps: [
      'Enter the URL of your receiver (http or https).',
      'HMAC (default): the credential is the shared key. Each request carries the header X-ACP-Signature with the value sha256=<hex>, the HMAC-SHA256 of the raw request body using the key. Without a credential no signature is sent.',
      'Bearer: the credential is sent as the header "Authorization: Bearer <token>". A credential is required in this mode.',
      'Every request also carries X-ACP-Event with the event kind (for example run.failed) and Content-Type application/json.',
      'Verify the signature on the raw body bytes before parsing JSON, and compare in constant time.',
      'Respond with a 2xx status; other status codes count as a failed delivery and are retried.',
    ],
    notes: [EGRESS_NOTE],
    example: {
      caption: 'Verify an HMAC signature in a shell (body saved as body.json)',
      code: 'printf "sha256=%s" "$(openssl dgst -sha256 -hmac "$KEY" -hex < body.json | sed \'s/^.* //\')"',
    },
  },
};

/** Per-field help texts keyed by field id. */
export const FIELD_HELP: Record<ChannelType, Record<string, string>> = {
  webhook: {
    url: 'Receiver endpoint; the event JSON is POSTed here.',
    auth: 'HMAC signs the body with the credential; Bearer sends the credential as Authorization header.',
  },
  matrix: {
    homeserver: 'Base URL of the homeserver, not the Element web address.',
    roomId:
      'Internal room ID (Room settings, Advanced), starting with "!". Aliases like #room:server do not work.',
  },
  smtp: {
    host: 'Hostname of the SMTP server.',
    port: 'Leave empty for the default of the selected security mode.',
    security: 'STARTTLS upgrades a plain connection (587); TLS is encrypted from the start (465).',
    username: 'Required when a password is set. Often the full e-mail address.',
    credential:
      'Use the mailbox or app password of this SMTP account, not your ACP login password.',
    from: 'Sender address. The server must allow it.',
    to: 'One or more recipients separated by commas.',
  },
  gotify: {
    url: 'Base URL of the Gotify server, without /message.',
    priority: 'Optional message priority; empty uses 5.',
  },
  ntfy: {
    url: 'ntfy server. Use https://ntfy.sh for the public service.',
    auth: 'Access token (tk_...) is sent as a Bearer token; username and password use HTTP basic authentication.',
    username: 'ntfy account name. Only used with username and password.',
    topic:
      'Single word without slashes. Choose an unguessable value; on public servers the topic is the only protection.',
    priority: '1 (min) to 5 (max); empty uses the default 3.',
  },
};

export const SMTP_DEFAULT_PORTS: Record<'starttls' | 'tls' | 'none', number> = {
  starttls: 587,
  tls: 465,
  none: 25,
};

export function smtpPortHint(security: 'starttls' | 'tls' | 'none'): string {
  return `Default for this mode: ${SMTP_DEFAULT_PORTS[security]}`;
}

export const NTFY_DEFAULT_URL = 'https://ntfy.sh';

/** Label for the credential input, depending on type (and webhook auth mode). */
export function credentialLabel(
  type: ChannelType,
  auth: 'hmac' | 'bearer' | 'token' | 'basic' = 'hmac',
): string {
  switch (type) {
    case 'smtp':
      return 'SMTP password';
    case 'gotify':
      return 'Application token';
    case 'matrix':
      return 'Access token';
    case 'ntfy':
      return auth === 'basic' ? 'ntfy password' : 'Access token (tk_...)';
    case 'webhook':
      return auth === 'bearer' ? 'Bearer token' : 'HMAC signing key';
  }
}

/** Secret type used when the form creates the credential. */
export function credentialSecretType(type: ChannelType): 'password' | 'token' | 'api_token' {
  if (type === 'smtp') return 'password';
  if (type === 'gotify') return 'api_token';
  return 'token';
}

/** Secret name proposed for a new channel credential. */
export function credentialSecretName(channelName: string): string {
  const slug = channelName
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return `notify-${slug || 'channel'}`;
}
