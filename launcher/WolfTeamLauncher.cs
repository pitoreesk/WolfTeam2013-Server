using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using System.Windows.Forms;

namespace WolfTeamLauncher
{
    public class LauncherForm : Form
    {
        private TextBox txtGameDir;
        private TextBox txtIp;
        private TextBox txtPort;
        private TextBox txtUser;
        private TextBox txtPass;
        private RadioButton rbDirectBin;
        private RadioButton rbNyxLauncher;
        private CheckBox chkApexBypass;
        private CheckBox chkBrokerRelay;
        private Label lblStatus;
        private Label lblApexStatus;
        private Label lblRelayStatus;
        private Button btnLaunch;
        private Button btnCheck;
        private Button btnBrowse;

        private readonly string configPath;
        private TcpListener apexListener;
        private CancellationTokenSource apexCts;
        private int apexHitCount = 0;

        private TcpListener brokerRelayListener;
        private TcpListener gameRelayListener;
        private CancellationTokenSource relayCts;
        private int relayConnCount = 0;

        // Legacy September 2012 TR client hardcoded broker endpoint (212.68.41.11:30706).
        // Bound strictly to Windows Loopback Pseudo-Interface 1 (-SkipAsSource $true)
        // so Ethernet/Wi-Fi DHCP settings are never modified.
        private const string LegacyBrokerIp = "212.68.41.11";

        public LauncherForm()
        {
            configPath = Path.Combine(AppDomain.CurrentDomain.BaseDirectory, "launcher_config.ini");

            Text = "WolfTeam 2012/2013 Private Server Launcher";
            Size = new Size(530, 505);
            FormBorderStyle = FormBorderStyle.FixedSingle;
            MaximizeBox = false;
            StartPosition = FormStartPosition.CenterScreen;
            BackColor = Color.FromArgb(24, 26, 32);
            ForeColor = Color.FromArgb(235, 238, 245);
            Font = new Font("Segoe UI", 9.5f);

            var lblTitle = new Label
            {
                Text = "WolfTeam 2012/2013 Private Server Launcher",
                Font = new Font("Segoe UI", 12.5f, FontStyle.Bold),
                ForeColor = Color.FromArgb(110, 185, 255),
                Location = new Point(20, 14),
                AutoSize = true
            };
            Controls.Add(lblTitle);

            int y = 55;
            AddLabel("Oyun Klasoru:", 20, y + 3);
            txtGameDir = AddTextBox(130, y, 290, DetectDefaultGameDir());
            btnBrowse = new Button
            {
                Text = "Sec...",
                Location = new Point(428, y - 1),
                Size = new Size(70, 26),
                FlatStyle = FlatStyle.Flat,
                BackColor = Color.FromArgb(48, 52, 64),
                ForeColor = Color.White
            };
            btnBrowse.FlatAppearance.BorderColor = Color.FromArgb(75, 82, 98);
            btnBrowse.Click += BtnBrowse_Click;
            Controls.Add(btnBrowse);

            y += 38;
            AddLabel("Sunucu IP:", 20, y + 3);
            txtIp = AddTextBox(130, y, 225, "127.0.0.1");
            AddLabel("Port:", 367, y + 3);
            txtPort = AddTextBox(410, y, 88, "40706");

            y += 38;
            AddLabel("Kullanici Adi:", 20, y + 3);
            txtUser = AddTextBox(130, y, 368, "");

            y += 38;
            AddLabel("Sifre:", 20, y + 3);
            txtPass = AddTextBox(130, y, 368, "");
            txtPass.UseSystemPasswordChar = true;

            y += 38;
            AddLabel("Baslatma Modu:", 20, y + 2);
            rbDirectBin = new RadioButton
            {
                Text = "Wolfteam.bin (Onerilen - Patch Bypass)",
                Location = new Point(130, y),
                AutoSize = true,
                Checked = true,
                ForeColor = Color.FromArgb(170, 235, 185)
            };
            Controls.Add(rbDirectBin);

            y += 24;
            rbNyxLauncher = new RadioButton
            {
                Text = "NyxLauncher.exe (_!!DEBUG!!_ ile)",
                Location = new Point(130, y),
                AutoSize = true,
                ForeColor = Color.FromArgb(200, 205, 215)
            };
            Controls.Add(rbNyxLauncher);

            y += 28;
            chkApexBypass = new CheckBox
            {
                Text = "Avital / update.u Bypass (Port 80 + Hosts)",
                Location = new Point(130, y),
                AutoSize = true,
                Checked = true,
                ForeColor = Color.FromArgb(145, 215, 255)
            };
            Controls.Add(chkApexBypass);

            y += 24;
            chkBrokerRelay = new CheckBox
            {
                Text = "2012 Broker Yonlendirme (Loopback -> Sunucu)",
                Location = new Point(130, y),
                AutoSize = true,
                Checked = true,
                ForeColor = Color.FromArgb(255, 215, 130)
            };
            Controls.Add(chkBrokerRelay);

            y += 34;
            btnCheck = new Button
            {
                Text = "Sunucu Kontrol",
                Location = new Point(20, y),
                Size = new Size(160, 38),
                FlatStyle = FlatStyle.Flat,
                BackColor = Color.FromArgb(48, 52, 64),
                ForeColor = Color.White,
                Font = new Font("Segoe UI", 9.5f, FontStyle.Bold)
            };
            btnCheck.FlatAppearance.BorderColor = Color.FromArgb(75, 82, 98);
            btnCheck.Click += async (s, e) => await CheckServerAsync();
            Controls.Add(btnCheck);

            btnLaunch = new Button
            {
                Text = "OYUNU BASLAT",
                Location = new Point(192, y),
                Size = new Size(306, 38),
                FlatStyle = FlatStyle.Flat,
                BackColor = Color.FromArgb(35, 130, 220),
                ForeColor = Color.White,
                Font = new Font("Segoe UI", 10.5f, FontStyle.Bold)
            };
            btnLaunch.FlatAppearance.BorderSize = 0;
            btnLaunch.Click += BtnLaunch_Click;
            Controls.Add(btnLaunch);

            y += 48;
            lblStatus = new Label
            {
                Text = "Hazir.",
                Location = new Point(20, y),
                Size = new Size(480, 22),
                ForeColor = Color.FromArgb(175, 182, 196)
            };
            Controls.Add(lblStatus);

            y += 24;
            lblApexStatus = new Label
            {
                Text = "Avital HTTP: Bekliyor...",
                Location = new Point(20, y),
                Size = new Size(480, 22),
                ForeColor = Color.FromArgb(165, 175, 190)
            };
            Controls.Add(lblApexStatus);

            y += 24;
            lblRelayStatus = new Label
            {
                Text = "Broker Relay: Bekliyor...",
                Location = new Point(20, y),
                Size = new Size(480, 34),
                ForeColor = Color.FromArgb(165, 175, 190)
            };
            Controls.Add(lblRelayStatus);

            AcceptButton = btnLaunch;
            LoadConfig();
            Shown += async (s, e) =>
            {
                if (chkApexBypass.Checked) StartApexBypass();
                if (chkBrokerRelay.Checked) await StartBrokerRelayAsync();
                await CheckServerAsync();
            };
            FormClosing += (s, e) =>
            {
                StopApexServer();
                StopBrokerRelay();
            };
        }

        private Label AddLabel(string text, int x, int y)
        {
            var lbl = new Label
            {
                Text = text,
                Location = new Point(x, y),
                AutoSize = true,
                ForeColor = Color.FromArgb(205, 210, 220)
            };
            Controls.Add(lbl);
            return lbl;
        }

        private TextBox AddTextBox(int x, int y, int width, string defaultText)
        {
            var tb = new TextBox
            {
                Location = new Point(x, y),
                Width = width,
                Text = defaultText,
                BackColor = Color.FromArgb(36, 39, 48),
                ForeColor = Color.White,
                BorderStyle = BorderStyle.FixedSingle
            };
            Controls.Add(tb);
            return tb;
        }

        private string DetectDefaultGameDir()
        {
            string[] candidates = new[]
            {
                AppDomain.CurrentDomain.BaseDirectory.TrimEnd('\\'),
                @"C:\Program Files (x86)\Joygame\WolfTeamTS",
                Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.Desktop), "WolfTeamTS")
            };

            foreach (string dir in candidates)
            {
                if (!string.IsNullOrEmpty(dir) && File.Exists(Path.Combine(dir, "Wolfteam.bin")))
                {
                    return dir;
                }
            }
            return @"C:\Program Files (x86)\Joygame\WolfTeamTS";
        }

        private void BtnBrowse_Click(object sender, EventArgs e)
        {
            using (var dlg = new FolderBrowserDialog())
            {
                dlg.Description = "Wolfteam.bin dosyasinin bulundugu oyun klasorunu secin";
                if (Directory.Exists(txtGameDir.Text))
                {
                    dlg.SelectedPath = txtGameDir.Text;
                }
                if (dlg.ShowDialog() == DialogResult.OK)
                {
                    txtGameDir.Text = dlg.SelectedPath;
                }
            }
        }

        // ==================== BROKER IP RELAY ====================

        private async Task StartBrokerRelayAsync()
        {
            if (brokerRelayListener != null)
            {
                SetRelayStatus(string.Format("Broker Relay: AKTIF ({0}:30706 -> {1}:{2})", LegacyBrokerIp, txtIp.Text.Trim(), txtPort.Text.Trim()), Color.FromArgb(115, 230, 145));
                return;
            }

            SetRelayStatus("Broker Relay: Loopback uzerine yonlendirme IP'si ekleniyor...", Color.FromArgb(240, 200, 95));

            bool ipAdded = await Task.Run(() => RunHiddenPowerShell(
                "if (-not (Get-NetIPAddress -InterfaceIndex 1 -IPAddress " + LegacyBrokerIp + " -ErrorAction SilentlyContinue)) { " +
                "New-NetIPAddress -InterfaceIndex 1 -IPAddress " + LegacyBrokerIp + " -PrefixLength 32 -SkipAsSource $true -ErrorAction Stop | Out-Null }"));

            if (!ipAdded)
            {
                SetRelayStatus("Broker Relay: Loopback IP eklenemedi (Yonetici izni gerekli).", Color.FromArgb(255, 115, 115));
                return;
            }

            await Task.Delay(350);

            try
            {
                IPAddress bindAddr = IPAddress.Parse(LegacyBrokerIp);
                relayCts = new CancellationTokenSource();

                brokerRelayListener = new TcpListener(bindAddr, 30706);
                brokerRelayListener.Start();
                var _brokerLoop = Task.Run(() => RelayAcceptLoop(brokerRelayListener, 30706, false, relayCts.Token));

                try
                {
                    gameRelayListener = new TcpListener(bindAddr, 40707);
                    gameRelayListener.Start();
                    var _gameLoop = Task.Run(() => RelayAcceptLoop(gameRelayListener, 40707, true, relayCts.Token));
                }
                catch
                {
                    gameRelayListener = null;
                }

                string targetIp = txtIp.Text.Trim();
                string targetPort = txtPort.Text.Trim();
                SetRelayStatus(string.Format("Broker Relay: AKTIF ({0}:30706 -> {1}:{2})", LegacyBrokerIp, targetIp, targetPort), Color.FromArgb(115, 230, 145));
                LogRelay(string.Format("RELAY_STARTED {0}:30706 -> {1}:{2}", LegacyBrokerIp, targetIp, targetPort));
            }
            catch (Exception ex)
            {
                StopBrokerRelay();
                SetRelayStatus("Broker Relay hatasi: " + ex.Message, Color.FromArgb(255, 115, 115));
                LogRelay("RELAY_START_ERR: " + ex.Message);
            }
        }

        private void StopBrokerRelay()
        {
            try
            {
                if (relayCts != null) relayCts.Cancel();
                if (brokerRelayListener != null) brokerRelayListener.Stop();
                if (gameRelayListener != null) gameRelayListener.Stop();
            }
            catch
            {
            }
            brokerRelayListener = null;
            gameRelayListener = null;

            RunHiddenPowerShell("Remove-NetIPAddress -InterfaceIndex 1 -IPAddress " + LegacyBrokerIp + " -Confirm:$false -ErrorAction SilentlyContinue");
        }

        private async Task RelayAcceptLoop(TcpListener listener, int localPort, bool isGamePort, CancellationToken ct)
        {
            while (!ct.IsCancellationRequested)
            {
                TcpClient client = null;
                try
                {
                    client = await listener.AcceptTcpClientAsync();
                    var captured = client;
                    var _ = Task.Run(() => HandleRelaySession(captured, localPort, isGamePort, ct));
                }
                catch
                {
                    if (client != null) client.Dispose();
                    if (ct.IsCancellationRequested) break;
                }
            }
        }

        private async Task HandleRelaySession(TcpClient localClient, int localPort, bool isGamePort, CancellationToken ct)
        {
            int connId = Interlocked.Increment(ref relayConnCount);
            string targetIp = "127.0.0.1";
            int targetPort = 40706;
            try
            {
                Invoke((Action)(() =>
                {
                    targetIp = txtIp.Text.Trim();
                    int p;
                    if (int.TryParse(txtPort.Text.Trim(), out p))
                    {
                        targetPort = isGamePort ? (p + 1) : p;
                    }
                }));
            }
            catch { }

            LogRelay(string.Format("#{0} ACCEPT local=:{1} -> remote={2}:{3}", connId, localPort, targetIp, targetPort));
            SetRelayStatus(string.Format("Broker Relay (#{0}): Baglanti aktariliyor -> {1}:{2}", connId, targetIp, targetPort), Color.FromArgb(115, 230, 145));

            using (localClient)
            using (var remoteClient = new TcpClient())
            {
                try
                {
                    localClient.NoDelay = true;
                    remoteClient.NoDelay = true;
                    await remoteClient.ConnectAsync(targetIp, targetPort);

                    using (var localStream = localClient.GetStream())
                    using (var remoteStream = remoteClient.GetStream())
                    {
                        var t1 = PumpStreamAsync(localStream, remoteStream, connId, "C->S", ct);
                        var t2 = PumpStreamAsync(remoteStream, localStream, connId, "S->C", ct);
                        await Task.WhenAny(t1, t2);
                    }
                }
                catch (Exception ex)
                {
                    LogRelay(string.Format("#{0} ERR: {1}", connId, ex.Message));
                }
            }
            LogRelay(string.Format("#{0} CLOSED", connId));
        }

        private async Task PumpStreamAsync(NetworkStream src, NetworkStream dst, int connId, string dir, CancellationToken ct)
        {
            byte[] buf = new byte[8192];
            while (!ct.IsCancellationRequested)
            {
                int n = await src.ReadAsync(buf, 0, buf.Length, ct);
                if (n <= 0) break;
                await dst.WriteAsync(buf, 0, n, ct);
                await dst.FlushAsync(ct);
            }
        }

        private static bool RunHiddenPowerShell(string script)
        {
            try
            {
                var psi = new ProcessStartInfo
                {
                    FileName = "powershell.exe",
                    Arguments = "-NoProfile -ExecutionPolicy Bypass -Command \"" + script.Replace("\"", "\\\"") + "\"",
                    UseShellExecute = false,
                    CreateNoWindow = true,
                    WindowStyle = ProcessWindowStyle.Hidden
                };
                using (var p = Process.Start(psi))
                {
                    p.WaitForExit(10000);
                    return p.ExitCode == 0;
                }
            }
            catch
            {
                return false;
            }
        }

        private void LogRelay(string line)
        {
            try
            {
                string logPath = Path.Combine(AppDomain.CurrentDomain.BaseDirectory, "broker_relay.log");
                File.AppendAllText(logPath, DateTime.Now.ToString("HH:mm:ss.fff") + " " + line + Environment.NewLine);
            }
            catch { }
        }

        // ==================== AVITAL HTTP BYPASS ====================

        private void EnsureHostsRedirect()
        {
            string hostsPath = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System), @"drivers\etc\hosts");
            string[] domains = new[]
            {
                "cdn.joygamedl.com",
                "fetch.joygame.com",
                "www.joygame.com",
                "joygame.com"
            };

            string content = File.Exists(hostsPath) ? File.ReadAllText(hostsPath) : "";
            var toAdd = new List<string>();
            foreach (string d in domains)
            {
                if (content.IndexOf(d, StringComparison.OrdinalIgnoreCase) < 0)
                {
                    toAdd.Add("127.0.0.1 " + d);
                }
            }

            if (toAdd.Count > 0)
            {
                string prefix = (content.Length > 0 && !content.EndsWith("\n")) ? Environment.NewLine : "";
                File.AppendAllText(hostsPath, prefix + "# WolfTeam Avital Bypass" + Environment.NewLine + string.Join(Environment.NewLine, toAdd) + Environment.NewLine);
            }
        }

        private void StartApexBypass()
        {
            try
            {
                EnsureHostsRedirect();
            }
            catch (Exception ex)
            {
                SetApexStatus("Hosts yazilamadi (Yonetici olarak calistirin): " + ex.Message, Color.FromArgb(255, 115, 115));
                return;
            }

            if (apexListener != null)
            {
                SetApexStatus("Avital HTTP: AKTIF (127.0.0.1:80 -> update.u hazir)", Color.FromArgb(115, 230, 145));
                return;
            }

            try
            {
                apexCts = new CancellationTokenSource();
                apexListener = new TcpListener(IPAddress.Loopback, 80);
                apexListener.Start();
                var _apexLoop = Task.Run(() => ApexAcceptLoop(apexCts.Token));
                SetApexStatus("Avital HTTP: AKTIF (127.0.0.1:80 -> update.u hazir)", Color.FromArgb(115, 230, 145));
            }
            catch (Exception ex)
            {
                apexListener = null;
                SetApexStatus("Port 80 dinlenemedi: " + ex.Message, Color.FromArgb(245, 195, 90));
            }
        }

        private void StopApexServer()
        {
            try
            {
                if (apexCts != null) apexCts.Cancel();
                if (apexListener != null) apexListener.Stop();
            }
            catch
            {
            }
            apexListener = null;
        }

        private async Task ApexAcceptLoop(CancellationToken ct)
        {
            while (!ct.IsCancellationRequested)
            {
                TcpClient client = null;
                try
                {
                    client = await apexListener.AcceptTcpClientAsync();
                    var captured = client;
                    var _ = Task.Run(() => HandleApexClient(captured));
                }
                catch
                {
                    if (client != null) client.Dispose();
                    if (ct.IsCancellationRequested) break;
                }
            }
        }

        private void HandleApexClient(TcpClient client)
        {
            using (client)
            {
                try
                {
                    client.ReceiveTimeout = 5000;
                    client.SendTimeout = 5000;
                    using (var stream = client.GetStream())
                    {
                        byte[] buf = new byte[8192];
                        int read = stream.Read(buf, 0, buf.Length);
                        if (read <= 0) return;

                        string reqText = Encoding.ASCII.GetString(buf, 0, read);
                        string[] lines = reqText.Split(new[] { "\r\n", "\n" }, StringSplitOptions.None);
                        string firstLine = lines.Length > 0 ? lines[0] : "";
                        string[] parts = firstLine.Split(' ');
                        string method = parts.Length > 0 ? parts[0].ToUpperInvariant() : "GET";
                        string path = parts.Length > 1 ? parts[1] : "/";

                        string rangeHeader = null;
                        foreach (string line in lines)
                        {
                            if (line.StartsWith("Range:", StringComparison.OrdinalIgnoreCase))
                            {
                                rangeHeader = line.Substring(6).Trim();
                                break;
                            }
                        }

                        byte[] body = ResolveApexFile(path);
                        string dateStr = DateTime.UtcNow.ToString("R");

                        if (body != null)
                        {
                            int total = body.Length;
                            int start = 0;
                            int end = total - 1;
                            bool isPartial = false;

                            if (!string.IsNullOrEmpty(rangeHeader) && rangeHeader.StartsWith("bytes=", StringComparison.OrdinalIgnoreCase))
                            {
                                string spec = rangeHeader.Substring(6).Trim();
                                int commaIdx = spec.IndexOf(',');
                                if (commaIdx > 0) spec = spec.Substring(0, commaIdx);
                                string[] rangeParts = spec.Split('-');
                                if (rangeParts.Length == 2)
                                {
                                    int parsedStart;
                                    int parsedEnd;
                                    if (int.TryParse(rangeParts[0], out parsedStart))
                                    {
                                        start = Math.Max(0, parsedStart);
                                        if (!string.IsNullOrEmpty(rangeParts[1]) && int.TryParse(rangeParts[1], out parsedEnd))
                                        {
                                            end = Math.Min(total - 1, parsedEnd);
                                        }
                                        else
                                        {
                                            end = total - 1;
                                        }
                                        if (start <= end && start < total)
                                        {
                                            isPartial = true;
                                        }
                                    }
                                }
                            }

                            int contentLen = isPartial ? (end - start + 1) : total;
                            string statusLine = isPartial ? "HTTP/1.1 206 Partial Content\r\n" : "HTTP/1.1 200 OK\r\n";
                            string extraRange = isPartial ? string.Format("Content-Range: bytes {0}-{1}/{2}\r\n", start, end, total) : "";

                            string header =
                                statusLine +
                                "Date: " + dateStr + "\r\n" +
                                "Server: Apache/2.4\r\n" +
                                "Accept-Ranges: bytes\r\n" +
                                extraRange +
                                "Content-Type: application/octet-stream\r\n" +
                                "Content-Length: " + contentLen + "\r\n" +
                                "Connection: close\r\n\r\n";

                            byte[] hdrBytes = Encoding.ASCII.GetBytes(header);
                            stream.Write(hdrBytes, 0, hdrBytes.Length);
                            if (method != "HEAD")
                            {
                                stream.Write(body, isPartial ? start : 0, contentLen);
                            }
                            stream.Flush();

                            int hits = Interlocked.Increment(ref apexHitCount);
                            string codeStr = isPartial ? "206" : "200";
                            LogApex(string.Format("#{0} {1} {2} Range=[{3}] -> {4} ({5} B)", hits, method, path, rangeHeader ?? "-", codeStr, contentLen));
                            SetApexStatus(string.Format("Avital HTTP (#{0}): {1} {2} -> {3} OK ({4} B)", hits, method, path, codeStr, contentLen), Color.FromArgb(115, 230, 145));
                        }
                        else
                        {
                            string resp = "HTTP/1.1 404 Not Found\r\nDate: " + dateStr + "\r\nContent-Length: 0\r\nConnection: close\r\n\r\n";
                            byte[] respBytes = Encoding.ASCII.GetBytes(resp);
                            stream.Write(respBytes, 0, respBytes.Length);
                            stream.Flush();
                            LogApex(string.Format("404 {0} {1} Range=[{2}]", method, path, rangeHeader ?? "-"));
                        }
                    }
                }
                catch (Exception ex)
                {
                    LogApex("ERR: " + ex.Message);
                }
            }
        }

        private void LogApex(string line)
        {
            try
            {
                string logPath = Path.Combine(AppDomain.CurrentDomain.BaseDirectory, "apex_http.log");
                File.AppendAllText(logPath, DateTime.Now.ToString("HH:mm:ss.fff") + " " + line + Environment.NewLine);
            }
            catch { }
        }

        private byte[] ResolveApexFile(string urlPath)
        {
            try
            {
                string cleanPath = urlPath.Split('?')[0].TrimStart('/');
                string fileName = Path.GetFileName(cleanPath);
                if (string.IsNullOrEmpty(fileName)) return null;

                string gameDir = "";
                Invoke((Action)(() => gameDir = txtGameDir.Text.Trim()));

                string[] searchPaths = new[]
                {
                    Path.Combine(gameDir, "avital", fileName),
                    Path.Combine(gameDir, "avital", "AXUP", fileName),
                    Path.Combine(gameDir, fileName)
                };

                foreach (string p in searchPaths)
                {
                    if (File.Exists(p))
                    {
                        return File.ReadAllBytes(p);
                    }
                }
            }
            catch { }
            return null;
        }

        // ==================== SERVER CHECK & LAUNCH ====================

        private async Task CheckServerAsync()
        {
            string ip = txtIp.Text.Trim();
            int port;
            if (!int.TryParse(txtPort.Text.Trim(), out port))
            {
                SetStatus("Gecersiz port numarasi.", Color.FromArgb(255, 115, 115));
                return;
            }

            btnCheck.Enabled = false;
            SetStatus(string.Format("Baglanti kontrol ediliyor ({0}:{1})...", ip, port), Color.FromArgb(240, 200, 95));

            bool brokerOk = await TestTcpAsync(ip, port, 2500);
            bool gameOk = await TestTcpAsync(ip, port + 1, 2500);

            if (brokerOk && gameOk)
            {
                SetStatus(string.Format("Sunucu ACIK: Broker ({0}) ve Game ({1}) erisilebilir.", port, port + 1), Color.FromArgb(115, 230, 145));
            }
            else if (brokerOk)
            {
                SetStatus(string.Format("Broker ({0}) ACIK, fakat Game ({1}) portuna ulasilamadi.", port, port + 1), Color.FromArgb(245, 195, 90));
            }
            else
            {
                SetStatus(string.Format("Sunucuya ulasilamadi ({0}:{1}). Sunucu veya firewall kontrol edilmeli.", ip, port), Color.FromArgb(255, 115, 115));
            }

            btnCheck.Enabled = true;
        }

        private static async Task<bool> TestTcpAsync(string host, int port, int timeoutMs)
        {
            try
            {
                using (var client = new TcpClient())
                {
                    var connectTask = client.ConnectAsync(host, port);
                    var completed = await Task.WhenAny(connectTask, Task.Delay(timeoutMs));
                    return completed == connectTask && client.Connected;
                }
            }
            catch
            {
                return false;
            }
        }

        private async void BtnLaunch_Click(object sender, EventArgs e)
        {
            string gameDir = txtGameDir.Text.Trim();
            string ip = txtIp.Text.Trim();
            string port = txtPort.Text.Trim();
            string user = txtUser.Text.Trim();
            string pass = txtPass.Text;

            if (!Directory.Exists(gameDir))
            {
                MessageBox.Show("Oyun klasoru bulunamadi:\n" + gameDir, "Hata", MessageBoxButtons.OK, MessageBoxIcon.Error);
                return;
            }

            string targetExe = rbDirectBin.Checked ? "Wolfteam.bin" : "NyxLauncher.exe";
            string fullPath = Path.Combine(gameDir, targetExe);

            if (!File.Exists(fullPath))
            {
                MessageBox.Show(targetExe + " dosyasi bulunamadi:\n" + fullPath, "Hata", MessageBoxButtons.OK, MessageBoxIcon.Error);
                return;
            }

            if (string.IsNullOrWhiteSpace(ip) || string.IsNullOrWhiteSpace(port))
            {
                MessageBox.Show("Sunucu IP ve Port bos olamaz.", "Uyari", MessageBoxButtons.OK, MessageBoxIcon.Warning);
                return;
            }

            if (string.IsNullOrWhiteSpace(user) || string.IsNullOrEmpty(pass))
            {
                MessageBox.Show("Kullanici adi ve sifre giriniz.", "Uyari", MessageBoxButtons.OK, MessageBoxIcon.Warning);
                return;
            }

            if (chkApexBypass.Checked) StartApexBypass();
            if (chkBrokerRelay.Checked) await StartBrokerRelayAsync();

            SaveConfig();

            try
            {
                string launchArgs = rbDirectBin.Checked
                    ? string.Format("{0} {1}", user, ToHexUtf8(pass))
                    : string.Format("_!!DEBUG!!_ {0} {1} {2} {3}", ip, port, user, pass);
                var psi = new ProcessStartInfo
                {
                    FileName = fullPath,
                    WorkingDirectory = gameDir,
                    Arguments = launchArgs,
                    UseShellExecute = false
                };

                Process.Start(psi);
                SetStatus(targetExe + " baslatildi (" + user + " -> " + ip + ":" + port + ").", Color.FromArgb(115, 230, 145));
            }
            catch (Exception ex)
            {
                SetStatus("Baslatma hatasi: " + ex.Message, Color.FromArgb(255, 115, 115));
                MessageBox.Show("Oyun baslatilamadi:\n" + ex.Message, "Hata", MessageBoxButtons.OK, MessageBoxIcon.Error);
            }
        }

        private static string ToHexUtf8(string s)
        {
            byte[] bytes = Encoding.UTF8.GetBytes(s);
            var sb = new StringBuilder(bytes.Length * 2);
            foreach (byte b in bytes) sb.Append(b.ToString("x2"));
            return sb.ToString();
        }

        private void SetStatus(string text, Color color)
        {
            if (InvokeRequired) { BeginInvoke((Action)(() => SetStatus(text, color))); return; }
            lblStatus.Text = text;
            lblStatus.ForeColor = color;
        }

        private void SetApexStatus(string text, Color color)
        {
            if (InvokeRequired) { BeginInvoke((Action)(() => SetApexStatus(text, color))); return; }
            lblApexStatus.Text = text;
            lblApexStatus.ForeColor = color;
        }

        private void SetRelayStatus(string text, Color color)
        {
            if (InvokeRequired) { BeginInvoke((Action)(() => SetRelayStatus(text, color))); return; }
            lblRelayStatus.Text = text;
            lblRelayStatus.ForeColor = color;
        }

        private void SaveConfig()
        {
            try
            {
                var lines = new[]
                {
                    "GameDir=" + txtGameDir.Text.Trim(),
                    "IP=" + txtIp.Text.Trim(),
                    "Port=" + txtPort.Text.Trim(),
                    "User=" + txtUser.Text.Trim(),
                    "Mode=" + (rbDirectBin.Checked ? "DirectBin" : "NyxLauncher")
                };
                File.WriteAllLines(configPath, lines);
            }
            catch { }
        }

        private void LoadConfig()
        {
            try
            {
                if (!File.Exists(configPath)) return;
                var map = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
                foreach (var line in File.ReadAllLines(configPath))
                {
                    int idx = line.IndexOf('=');
                    if (idx > 0)
                    {
                        map[line.Substring(0, idx).Trim()] = line.Substring(idx + 1).Trim();
                    }
                }
                if (map.ContainsKey("GameDir") && Directory.Exists(map["GameDir"])) txtGameDir.Text = map["GameDir"];
                if (map.ContainsKey("IP") && !string.IsNullOrEmpty(map["IP"])) txtIp.Text = map["IP"];
                if (map.ContainsKey("Port") && !string.IsNullOrEmpty(map["Port"])) txtPort.Text = map["Port"];
                if (map.ContainsKey("User") && !string.IsNullOrEmpty(map["User"])) txtUser.Text = map["User"];
                if (map.ContainsKey("Mode") && map["Mode"].Equals("NyxLauncher", StringComparison.OrdinalIgnoreCase))
                {
                    rbNyxLauncher.Checked = true;
                }
            }
            catch { }
        }

        [STAThread]
        public static void Main()
        {
            Application.EnableVisualStyles();
            Application.SetCompatibleTextRenderingDefault(false);
            Application.Run(new LauncherForm());
        }
    }
}
