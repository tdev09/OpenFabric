package api

import (
	"strings"

	"github.com/openfabric/openfabric/internal/config"
)

// JoinPageHTML is the HTML template for the onboarding landing page.
const JoinPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Join {{PROJECT_NAME}} Cluster</title>
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500;600&display=swap" rel="stylesheet">
    <style>
        :root {
            --bg-primary: #0D1117;
            --bg-secondary: #161B22;
            --bg-card: #1C2128;
            --accent: #00C9A7;
            --accent-glow: rgba(0, 201, 167, 0.2);
            --text-primary: #E6EDF3;
            --text-secondary: #A8B2C1;
            --text-muted: #6E7681;
            --border: rgba(240, 246, 252, 0.1);
            --border-accent: rgba(0, 201, 167, 0.3);
            --radius-md: 10px;
            --radius-lg: 16px;
            --font-sans: 'Inter', system-ui, -apple-system, sans-serif;
            --font-mono: 'JetBrains Mono', 'Fira Code', monospace;
        }

        * {
            box-sizing: border-box;
            margin: 0;
            padding: 0;
        }

        body {
            font-family: var(--font-sans);
            background: var(--bg-primary);
            color: var(--text-primary);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 24px;
            line-height: 1.6;
        }

        .container {
            width: 100%;
            max-width: 580px;
            background: var(--bg-secondary);
            border: 1px solid var(--border);
            border-radius: var(--radius-lg);
            padding: 40px;
            box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
            text-align: center;
            position: relative;
            overflow: hidden;
        }

        .container::before {
            content: '';
            position: absolute;
            top: 0;
            left: 0;
            right: 0;
            height: 4px;
            background: linear-gradient(90deg, var(--accent), #00e0bc);
        }

        .logo-area {
            display: flex;
            align-items: center;
            justify-content: center;
            gap: 12px;
            margin-bottom: 32px;
        }

        .logo-icon {
            font-size: 32px;
            color: var(--accent);
        }

        .logo-text {
            font-size: 24px;
            font-weight: 700;
            letter-spacing: -0.02em;
        }

        h1 {
            font-size: 24px;
            font-weight: 600;
            margin-bottom: 12px;
        }

        .subtitle {
            color: var(--text-secondary);
            font-size: 15px;
            margin-bottom: 32px;
        }

        .join-code-container {
            background: var(--bg-card);
            border: 1px dashed var(--border-accent);
            border-radius: var(--radius-md);
            padding: 16px;
            margin-bottom: 32px;
            cursor: pointer;
            transition: all 150ms ease;
        }

        .join-code-container:hover {
            border-color: var(--accent);
            box-shadow: 0 0 16px var(--accent-glow);
        }

        .join-code-label {
            font-size: 11px;
            color: var(--text-muted);
            text-transform: uppercase;
            letter-spacing: 0.05em;
            margin-bottom: 4px;
        }

        .join-code {
            font-family: var(--font-mono);
            font-size: 24px;
            font-weight: 600;
            color: var(--accent);
            letter-spacing: 0.05em;
        }

        .btn {
            display: inline-flex;
            align-items: center;
            justify-content: center;
            gap: 8px;
            width: 100%;
            padding: 14px 24px;
            background: var(--accent);
            color: #0D1117;
            border: 1px solid var(--accent);
            border-radius: var(--radius-md);
            font-size: 15px;
            font-weight: 600;
            text-decoration: none;
            cursor: pointer;
            transition: all 150ms ease;
            margin-bottom: 16px;
        }

        .btn:hover {
            background: #00e0bc;
            border-color: #00e0bc;
            box-shadow: 0 0 16px var(--accent-glow);
        }

        .btn-secondary {
            background: transparent;
            color: var(--text-primary);
            border: 1px solid var(--border);
        }

        .btn-secondary:hover {
            background: var(--bg-card);
            border-color: var(--accent);
            color: var(--accent);
            box-shadow: none;
        }

        .code-box {
            background: var(--bg-card);
            border: 1px solid var(--border);
            border-radius: var(--radius-md);
            padding: 14px;
            font-family: var(--font-mono);
            font-size: 13px;
            text-align: left;
            position: relative;
            margin-bottom: 16px;
            overflow-x: auto;
            white-space: nowrap;
        }

        .copy-btn {
            position: absolute;
            right: 8px;
            top: 50%;
            transform: translateY(-50%);
            background: var(--bg-secondary);
            border: 1px solid var(--border);
            color: var(--text-secondary);
            padding: 4px 8px;
            border-radius: 4px;
            font-size: 11px;
            cursor: pointer;
            transition: all 150ms ease;
        }

        .copy-btn:hover {
            color: var(--accent);
            border-color: var(--accent);
        }

        .instructions {
            color: var(--text-secondary);
            font-size: 14px;
            text-align: left;
            margin-top: 24px;
            border-top: 1px solid var(--border);
            padding-top: 24px;
        }

        .instructions ol {
            padding-left: 20px;
            margin-top: 8px;
        }

        .instructions li {
            margin-bottom: 8px;
        }

        .os-section {
            display: none;
        }

        .os-section.active {
            display: block;
        }

        .badge-pi {
            display: inline-block;
            background: rgba(197, 17, 98, 0.15);
            color: #FF2A85;
            padding: 2px 8px;
            border-radius: 4px;
            font-size: 11px;
            font-weight: 600;
            margin-bottom: 12px;
        }

        .toast {
            position: fixed;
            bottom: 24px;
            left: 50%;
            transform: translateX(-50%) translateY(100px);
            background: var(--accent);
            color: #0D1117;
            padding: 12px 24px;
            border-radius: var(--radius-md);
            font-size: 14px;
            font-weight: 500;
            box-shadow: 0 4px 16px rgba(0,0,0,0.3);
            transition: transform 300ms cubic-bezier(0.4, 0, 0.2, 1);
            z-index: 1000;
        }

        .toast.show {
            transform: translateX(-50%) translateY(0);
        }
    </style>
</head>
<body>

    <div class="container">
        <div class="logo-area">
            <span class="logo-icon">✦</span>
            <span class="logo-text">{{PROJECT_NAME}}</span>
        </div>

        <h1>Join the Cluster</h1>
        <p class="subtitle">Connect this device to the cluster hosted by <strong>{{COORDINATOR_IP}}</strong></p>

        <div class="join-code-container" onclick="copyText('{{JOIN_CODE}}', 'Join code copied!')">
            <div class="join-code-label">Click to copy join code</div>
            <div class="join-code">{{JOIN_CODE}}</div>
        </div>

        <!-- MAC SECTION -->
        <div id="os-mac" class="os-section">
            <a href="http://127.0.0.1:4892" target="_blank" class="btn">
                ✦ Open Local Agent (http://127.0.0.1:4892)
            </a>
            <a href="{{RELEASES_URL}}" class="btn btn-secondary" style="margin-bottom:16px;">
                Download {{PROJECT_NAME}} for Mac
            </a>
            <div class="instructions">
                <strong>After downloading:</strong>
                <ol>
                    <li>Run the downloaded file in your terminal:<br><code>./fabric-darwin-arm64</code> (or amd64)</li>
                    <li>Your browser will <strong>open automatically</strong> at <code>http://localhost:4892</code></li>
                    <li>Go to <strong>Devices → Join a cluster</strong></li>
                    <li>Enter coordinator IP <code>{{COORDINATOR_IP}}</code> and join code <code>{{JOIN_CODE}}</code></li>
                </ol>
            </div>
        </div>

        <!-- WINDOWS SECTION -->
        <div id="os-windows" class="os-section">
            <a href="http://127.0.0.1:4892" target="_blank" class="btn">
                ✦ Open Local Agent (http://127.0.0.1:4892)
            </a>
            <a href="{{RELEASES_URL}}" class="btn btn-secondary" style="margin-bottom:16px;">
                Download {{PROJECT_NAME}} for Windows
            </a>
            <div class="instructions">
                <strong>After downloading:</strong>
                <ol>
                    <li>Double-click <code>fabric-windows-amd64.exe</code> or run it in CMD/PowerShell</li>
                    <li>Your browser will <strong>open automatically</strong> at <code>http://localhost:4892</code></li>
                    <li>Go to <strong>Devices → Join a cluster</strong></li>
                    <li>Enter coordinator IP <code>{{COORDINATOR_IP}}</code> and join code <code>{{JOIN_CODE}}</code></li>
                </ol>
            </div>
        </div>

        <!-- LINUX / PI SECTION -->
        <div id="os-linux" class="os-section">
            <div class="badge-pi" id="pi-badge" style="display:none;">Raspberry Pi Detected</div>
            <div class="code-box">
                <span id="cli-cmd">fabric join {{COORDINATOR_IP}} --token {{TOKEN}}</span>
                <button class="copy-btn" onclick="copyText('fabric join {{COORDINATOR_IP}} --token {{TOKEN}}', 'CLI command copied!')">Copy</button>
            </div>
            <div class="instructions">
                <strong>Next Steps:</strong>
                <ol>
                    <li>Ensure {{PROJECT_NAME}} is installed on your Linux device.</li>
                    <li>Copy and run the command above in your terminal.</li>
                    <li>The agent will automatically join the cluster.</li>
                </ol>
            </div>
        </div>

        <!-- MOBILE SECTION -->
        <div id="os-mobile" class="os-section">
            <a href="http://{{COORDINATOR_IP}}:4892" class="btn">
                View Cluster Dashboard
            </a>
            <div class="instructions" style="text-align: center;">
                <strong>Mobile Node support is coming soon.</strong><br>
                For now, you can view and control the cluster dashboard directly from your browser.
            </div>
        </div>

        <!-- UNKNOWN / FALLBACK SECTION -->
        <div id="os-unknown" class="os-section">
            <a href="http://{{COORDINATOR_IP}}:4892" class="btn" style="margin-bottom:12px;">
                View Dashboard (This Device)
            </a>
            <p style="margin: 16px 0; color: var(--text-muted);">or select your platform to install node:</p>
            <a href="{{RELEASES_URL}}" class="btn btn-secondary" style="margin-bottom:12px;">
                Download Desktop App (Mac / Win)
            </a>
            <div class="code-box">
                <span>fabric join {{COORDINATOR_IP}} --token {{TOKEN}}</span>
                <button class="copy-btn" onclick="copyText('fabric join {{COORDINATOR_IP}} --token {{TOKEN}}', 'CLI command copied!')">Copy</button>
            </div>
        </div>
    </div>

    <div id="toast" class="toast">Copied to clipboard!</div>

    <script>
        function detectOS() {
            var ua = navigator.userAgent.toLowerCase();
            
            // Mobile detection
            if (/android|iphone|ipad|ipod|windows phone/i.test(ua)) {
                return 'mobile';
            }
            
            // OS detection
            if (ua.indexOf('macintosh') !== -1 || ua.indexOf('mac os') !== -1) {
                return 'mac';
            }
            if (ua.indexOf('windows') !== -1) {
                return 'windows';
            }
            if (ua.indexOf('linux') !== -1) {
                // Check if Raspberry Pi
                if (ua.indexOf('arm') !== -1 || ua.indexOf('raspberrypi') !== -1) {
                    document.getElementById('pi-badge').style.display = 'inline-block';
                }
                return 'linux';
            }
            
            return 'unknown';
        }

        var os = detectOS();
        document.getElementById('os-' + os).classList.add('active');

        function copyText(text, message) {
            navigator.clipboard.writeText(text).then(function() {
                showToast(message || "Copied to clipboard!");
            });
        }

        function showToast(message) {
            var toast = document.getElementById('toast');
            toast.innerText = message;
            toast.classList.add('show');
            setTimeout(function() {
                toast.classList.remove('show');
            }, 2500);
        }
    </script>
</body>
</html>
`

// RenderJoinPage replaces template variables in the JoinPageHTML.
func RenderJoinPage(token, coordinatorIP string) string {
	res := JoinPageHTML
	res = strings.ReplaceAll(res, "{{PROJECT_NAME}}", config.ProjectName)
	res = strings.ReplaceAll(res, "{{RELEASES_URL}}", config.ReleasesURL)
	res = strings.ReplaceAll(res, "{{TOKEN}}", token)
	res = strings.ReplaceAll(res, "{{JOIN_CODE}}", "fabric-"+token)
	res = strings.ReplaceAll(res, "{{COORDINATOR_IP}}", coordinatorIP)
	return res
}
