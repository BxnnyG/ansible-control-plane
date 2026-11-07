/** Generic, detail-free messages for the ?error= codes set by the login flow. */
const MESSAGES: Record<string, string> = {
  login_failed: 'Sign-in failed. Please try again.',
  idp_unavailable: 'The identity provider is currently unavailable. Please try again later.',
  rate_limited: 'Too many sign-in attempts. Please wait a minute and try again.',
  account_disabled: 'This account is disabled. Contact an administrator.',
  no_access: 'Your account has no access to this application. Contact an administrator.',
};

/** Returns the message for a known error code, or null for no/unknown codes. */
export function loginErrorMessage(code: string | null | undefined): string | null {
  if (!code || !Object.hasOwn(MESSAGES, code)) return null;
  return MESSAGES[code];
}
