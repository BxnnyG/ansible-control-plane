import { describe, expect, it } from 'vitest';
import {
  FIELD_HELP,
  GUIDES,
  credentialLabel,
  credentialSecretName,
  credentialSecretType,
  smtpPortHint,
} from './notificationGuides';
import { CHANNEL_SECRET_TYPES, CHANNEL_TYPES } from './notifications';

describe('guides', () => {
  it('covers every channel type with steps, notes and field help', () => {
    for (const { type } of CHANNEL_TYPES) {
      expect(GUIDES[type].steps.length).toBeGreaterThan(2);
      expect(GUIDES[type].notes.some((n) => n.includes('egress'))).toBe(true);
      expect(Object.keys(FIELD_HELP[type]).length).toBeGreaterThan(0);
    }
  });

  it('states the Matrix encryption limitation', () => {
    expect(GUIDES.matrix.notes.join(' ')).toMatch(/Encrypted rooms are NOT supported/);
  });

  it('documents the webhook header names as implemented', () => {
    const text = GUIDES.webhook.steps.join(' ');
    expect(text).toContain('X-ACP-Signature');
    expect(text).toContain('sha256=');
    expect(text).toContain('X-ACP-Event');
    expect(text).toContain('Authorization: Bearer');
  });
});

describe('credential helpers', () => {
  it('labels per type', () => {
    expect(credentialLabel('smtp')).toBe('SMTP password');
    expect(credentialLabel('gotify')).toBe('Application token');
    expect(credentialLabel('webhook', 'bearer')).toBe('Bearer token');
    expect(credentialLabel('webhook', 'hmac')).toBe('HMAC signing key');
  });

  it('uses secret types the channel form accepts', () => {
    for (const { type } of CHANNEL_TYPES) {
      expect(CHANNEL_SECRET_TYPES).toContain(credentialSecretType(type));
    }
  });

  it('derives a safe secret name', () => {
    expect(credentialSecretName('  Ops Alerts / #1 ')).toBe('notify-ops-alerts-1');
    expect(credentialSecretName('!!!')).toBe('notify-channel');
  });

  it('hints the default SMTP port', () => {
    expect(smtpPortHint('starttls')).toContain('587');
    expect(smtpPortHint('tls')).toContain('465');
    expect(smtpPortHint('none')).toContain('25');
  });
});
