// SPDX-License-Identifier: Apache-2.0

package bootstrap

// suspendedHTML is the static page served when a domain is suspended.
// Placeholders: {{LOGO_URL}} and {{CONTACT_URL}}.
const suspendedHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0"/>
  <title>Account Suspended</title>
  <style>
    *, *::before, *::after { margin: 0; padding: 0; box-sizing: border-box; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
      background: #f0f2f5; min-height: 100vh;
      display: flex; flex-direction: column; align-items: center; justify-content: center;
      color: #1a202c;
    }
    .wrapper { width: 90%; max-width: 520px; }
    .card { background: #fff; border-radius: 16px; overflow: hidden; box-shadow: 0 8px 40px rgba(0,0,0,0.10); }
    .card-top { background: #c0392b; padding: 36px 40px 28px; text-align: center; }
    .logo { height: 52px; width: auto; margin-bottom: 20px; filter: brightness(0) invert(1); }
    .status-badge {
      display: inline-flex; align-items: center; gap: 8px;
      background: rgba(255,255,255,0.18); border: 1px solid rgba(255,255,255,0.35);
      border-radius: 999px; padding: 6px 16px; font-size: 13px; font-weight: 600;
      color: #fff; letter-spacing: 0.04em; text-transform: uppercase;
    }
    .status-badge::before { content: ''; width: 8px; height: 8px; border-radius: 50%; background: #fff; opacity: 0.85; }
    .card-body { padding: 36px 40px 40px; text-align: center; }
    .icon-wrap { width: 72px; height: 72px; border-radius: 50%; background: #fff5f5; display: flex; align-items: center; justify-content: center; margin: 0 auto 24px; font-size: 32px; }
    h1 { font-size: 22px; font-weight: 700; margin-bottom: 12px; color: #1a202c; }
    p { font-size: 15px; line-height: 1.75; color: #555e6d; margin-bottom: 10px; }
    .divider { border: none; border-top: 1px solid #edf0f4; margin: 28px 0; }
    .contact-box { background: #f8f9fc; border: 1px solid #e2e6ed; border-radius: 10px; padding: 20px 24px; text-align: left; }
    .contact-box .label { font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.08em; color: #9aa3af; margin-bottom: 10px; }
    .contact-box a { display: inline-flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 600; color: #c0392b; text-decoration: none; }
    .contact-box a:hover { text-decoration: underline; }
    .footer { margin-top: 24px; text-align: center; font-size: 12px; color: #9aa3af; }
    .footer a { color: #9aa3af; text-decoration: none; }
    .footer a:hover { text-decoration: underline; }
  </style>
</head>
<body>
  <div class="wrapper">
    <div class="card">
      <div class="card-top">
        <img class="logo" src="{{LOGO_URL}}" alt="Company Logo" />
        <div class="status-badge">Account Suspended</div>
      </div>
      <div class="card-body">
        <div class="icon-wrap">🔒</div>
        <h1>Your account has been suspended</h1>
        <p>Access to this service has been temporarily disabled.</p>
        <p>This may be due to an overdue invoice, a policy issue, or a request from your organization.</p>
        <hr class="divider" />
        <div class="contact-box">
          <div class="label">Need help? Contact support</div>
          <a href="{{CONTACT_URL}}">🌐 Contact Support</a>
        </div>
      </div>
    </div>
    <div class="footer">Managed by suctl</div>
  </div>
</body>
</html>
`

// maintenanceHTML is the static page served when a domain is in maintenance mode.
// Placeholders: {{LOGO_URL}} and {{CONTACT_URL}}.
const maintenanceHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0"/>
  <title>Scheduled Maintenance</title>
  <style>
    *, *::before, *::after { margin: 0; padding: 0; box-sizing: border-box; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
      background: #f0f2f5; min-height: 100vh;
      display: flex; flex-direction: column; align-items: center; justify-content: center;
      color: #1a202c;
    }
    .wrapper { width: 90%; max-width: 520px; }
    .card { background: #fff; border-radius: 16px; overflow: hidden; box-shadow: 0 8px 40px rgba(0,0,0,0.10); }
    .card-top { background: #d35400; padding: 36px 40px 28px; text-align: center; }
    .logo { height: 52px; width: auto; margin-bottom: 20px; filter: brightness(0) invert(1); }
    .status-badge {
      display: inline-flex; align-items: center; gap: 8px;
      background: rgba(255,255,255,0.18); border: 1px solid rgba(255,255,255,0.35);
      border-radius: 999px; padding: 6px 16px; font-size: 13px; font-weight: 600;
      color: #fff; letter-spacing: 0.04em; text-transform: uppercase;
    }
    .status-badge::before { content: ''; width: 8px; height: 8px; border-radius: 50%; background: #fff; opacity: 0.85; }
    .card-body { padding: 36px 40px 40px; text-align: center; }
    .icon-wrap { width: 72px; height: 72px; border-radius: 50%; background: #fff8f0; display: flex; align-items: center; justify-content: center; margin: 0 auto 24px; font-size: 32px; }
    h1 { font-size: 22px; font-weight: 700; margin-bottom: 12px; color: #1a202c; }
    p { font-size: 15px; line-height: 1.75; color: #555e6d; margin-bottom: 10px; }
    .divider { border: none; border-top: 1px solid #edf0f4; margin: 28px 0; }
    .contact-box { background: #f8f9fc; border: 1px solid #e2e6ed; border-radius: 10px; padding: 20px 24px; text-align: left; }
    .contact-box .label { font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.08em; color: #9aa3af; margin-bottom: 10px; }
    .contact-box a { display: inline-flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 600; color: #d35400; text-decoration: none; }
    .contact-box a:hover { text-decoration: underline; }
    .footer { margin-top: 24px; text-align: center; font-size: 12px; color: #9aa3af; }
    .footer a { color: #9aa3af; text-decoration: none; }
    .footer a:hover { text-decoration: underline; }
  </style>
</head>
<body>
  <div class="wrapper">
    <div class="card">
      <div class="card-top">
        <img class="logo" src="{{LOGO_URL}}" alt="Company Logo" />
        <div class="status-badge">Scheduled Maintenance</div>
      </div>
      <div class="card-body">
        <div class="icon-wrap">🛠️</div>
        <h1>We'll be back shortly</h1>
        <p>This service is currently undergoing scheduled maintenance.</p>
        <p>We apologise for the inconvenience. The system will be restored as soon as possible.</p>
        <hr class="divider" />
        <div class="contact-box">
          <div class="label">Questions? Contact support</div>
          <a href="{{CONTACT_URL}}">🌐 Contact Support</a>
        </div>
      </div>
    </div>
    <div class="footer">Managed by suctl</div>
  </div>
</body>
</html>
`
