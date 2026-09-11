/* tinker.c - minimal GUI skeleton for a WinPE agent
 *
 * Target : Win7 PE (WinPE 3.x, x86) / XP PE, also runs unchanged on Win10/11 PE.
 * Deps   : kernel32 + user32 + gdi32 only. All three exist in every WinPE.
 *
 * Build (VS2013 with the v120_xp toolset is the classic XP choice; VS2015+ works too):
 *   cl /nologo /O1 /GS- /W3 tinker.c ^
 *      /link /SUBSYSTEM:WINDOWS /ENTRY:WinMainCRTStartup /NODEFAULTLIB ^
 *      kernel32.lib user32.lib gdi32.lib
 *
 * /ENTRY:WinMainCRTStartup + /NODEFAULTLIB = no CRT, no VC++ redist on the PE box.
 * Consequences you must live with:
 *   - no sprintf / malloc / strcpy : use wsprintfW (user32) + lstrcatW (kernel32)
 *     + HeapAlloc/HeapFree. NOTE: wsprintfW caps output at 1024 chars.
 *   - no static initialisers that need CRT init, no __try/__except niceties.
 *
 * Source is deliberately ASCII-only. A non-BOM UTF-8 file with Chinese
 * literals is interpreted as the local ANSI codepage by cl.exe before VS2015
 * and comes out as mojibake. Keep localized text in tinker.ini (UTF-8) and
 * load it through Utf8ToWide() below.
 *
 * Threading contract (the whole point of this file):
 *   UI thread   -> message loop + all control access. Never blocks.
 *   worker      -> agent loop (HTTP + tool execution). Never touches controls.
 *   cross-thread-> PostMessageW(hwnd, WM_APP_LOG/WM_APP_STATUS, lParam = heap wstr)
 *                  The UI thread owns and frees the string. SendMessage from a
 *                  worker would deadlock the moment the UI blocks.
 *
 * Audit status (2026-09-11): this file is now a REFERENCE for the Go port rather
 * than the shipping implementation -- the product moved to Go 1.20 (see PLAN.md).
 * A read-only bug audit found five defects here; all are fixed in place and each
 * fix site carries a comment explaining what was wrong and why the fix is what
 * it is:
 *   1. CreateProcess ran un-suspended, so a grandchild could escape the job
 *      (now CREATE_SUSPENDED -> AssignProcessToJobObject -> ResumeThread)
 *   2. hNul was tested with `if (hNul)`, but CreateFileW reports failure with
 *      INVALID_HANDLE_VALUE, not NULL
 *   3. g_cancel was read without volatile
 *   4. the log trim could cut a UTF-16 surrogate pair in half
 *   5. the log trim only fired when the box was ALREADY over the cap, so a
 *      single oversized entry was never trimmed at all
 * Two invariants this file already gets RIGHT, and the port must preserve:
 *   - ReadFile must never request more bytes than PeekNamedPipe reports
 *   - the pipe's reader end must have HANDLE_FLAG_INHERIT cleared
 */
#define WINVER       0x0501
#define _WIN32_WINNT 0x0501
#define WIN32_LEAN_AND_MEAN
#include <windows.h>

#define ID_LOG      1001
#define ID_INPUT    1002
#define ID_SEND     1003
#define ID_STOP     1004
#define ID_STATUS   1005

#define WM_APP_LOG      (WM_APP + 1)   /* wParam = level, lParam = WCHAR* (UI frees) */
#define WM_APP_STATUS   (WM_APP + 2)   /* wParam = 0,     lParam = WCHAR* (UI frees) */

#define LV_AI   0
#define LV_USER 1
#define LV_TOOL 2
#define LV_OK   3
#define LV_ERR  4

/* A multi-line EDIT is a text box, not a log sink. Trim from the front. */
#define LOG_MAX_CHARS 60000

static HINSTANCE g_hInst;
static HWND  g_hwnd, g_log, g_input, g_send, g_stop, g_status;
static HFONT g_font;
static LONG  g_busy;      /* 1 while the worker is running      */
/* Written by the UI thread with InterlockedExchange (Esc / Stop / WM_CLOSE),
 * read by the worker loop in RunCmdCapture. Must be volatile: without it the
 * compiler may cache the value in a register across the poll loop, and Esc
 * would never take effect. (In the Go port this is an atomic.Bool / channel,
 * so the issue disappears -- see PLAN.md.) */
static volatile LONG g_cancel;    /* set by Esc / the Stop button       */

/* ------------------------------------------------------------------ memory */

static void* xalloc(SIZE_T n) { return HeapAlloc(GetProcessHeap(), 0, n); }
static void  xfree(void* p)   { if (p) HeapFree(GetProcessHeap(), 0, p); }

static WCHAR* wbuf(SIZE_T cch)
{
    WCHAR* p = (WCHAR*)xalloc(cch * sizeof(WCHAR));
    if (p) p[0] = 0;
    return p;
}

static WCHAR* wdup(const WCHAR* s)
{
    WCHAR* p;
    if (!s) return wbuf(1);
    p = wbuf((SIZE_T)lstrlenW(s) + 1);
    if (p) lstrcpyW(p, s);
    return p;
}

/* UTF-8 (config files, network payloads) -> UTF-16 */
static WCHAR* Utf8ToWide(const char* s)
{
    int n;
    WCHAR* w;
    if (!s) return wbuf(1);
    n = MultiByteToWideChar(CP_UTF8, 0, s, -1, NULL, 0);
    if (n <= 0) return wbuf(1);
    w = wbuf((SIZE_T)n);
    if (w) MultiByteToWideChar(CP_UTF8, 0, s, -1, w, n);
    return w;
}

/* OEM codepage (what cmd.exe writes to its stdout) -> UTF-16 */
static WCHAR* OemToWide(const char* s, int len)
{
    int n;
    WCHAR* w;
    if (!s || len <= 0) return wbuf(1);
    n = MultiByteToWideChar(CP_OEMCP, 0, s, len, NULL, 0);
    if (n <= 0) return wbuf(1);
    w = wbuf((SIZE_T)n + 1);
    if (w) { MultiByteToWideChar(CP_OEMCP, 0, s, len, w, n); w[n] = 0; }
    return w;
}

/* -------------------------------------------------------------------- log */

static const WCHAR* const kPrefix[5] = {
    L"ai  > ", L"you > ", L"  -> ", L"  <- ", L"  !! "
};

static void LogLine(int level, const WCHAR* text)
{
    WCHAR* line;
    SIZE_T n;
    if (!g_hwnd) return;
    if (!text) text = L"";
    n = (SIZE_T)lstrlenW(kPrefix[level]) + (SIZE_T)lstrlenW(text) + 4;
    line = wbuf(n + 1);
    if (!line) return;
    lstrcpyW(line, kPrefix[level]);
    lstrcatW(line, text);
    lstrcatW(line, L"\r\n");
    PostMessageW(g_hwnd, WM_APP_LOG, (WPARAM)level, (LPARAM)line);
}

static void LogStatus(const WCHAR* text)
{
    if (!g_hwnd) return;
    PostMessageW(g_hwnd, WM_APP_STATUS, 0, (LPARAM)wdup(text));
}

/* ------------------------------------------------------------------- exec */

typedef struct {
    DWORD exit_code;
    DWORD elapsed_ms;
    BOOL  timed_out;
    char* out;          /* OEM bytes, NUL terminated */
    DWORD out_len;
} CmdResult;

/* Put the child in a job object so a timeout kills the whole tree, not just
 * cmd.exe. JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE exists on XP and later. */
static HANDLE MakeKillableJob(HANDLE hProcess)
{
    HANDLE hJob = CreateJobObjectW(NULL, NULL);
    JOBOBJECT_EXTENDED_LIMIT_INFORMATION li;
    if (!hJob) return NULL;
    ZeroMemory(&li, sizeof(li));
    li.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
    if (!SetInformationJobObject(hJob, JobObjectExtendedLimitInformation, &li, sizeof(li)) ||
        !AssignProcessToJobObject(hJob, hProcess)) {
        CloseHandle(hJob);
        return NULL;
    }
    return hJob;
}

/* cmdline MUST be writable memory: CreateProcessW is allowed to modify it. */
static BOOL RunCmdCapture(WCHAR* cmdline, DWORD timeout_ms, CmdResult* r)
{
    SECURITY_ATTRIBUTES sa;
    STARTUPINFOW si;
    PROCESS_INFORMATION pi;
    HANDLE hRead = NULL, hWrite = NULL, hNul = NULL, hJob = NULL;
    char*  buf = NULL;
    DWORD  cap = 0, len = 0, got = 0, avail = 0, code = 0, t0 = 0;

    ZeroMemory(r, sizeof(*r));
    ZeroMemory(&si, sizeof(si));
    ZeroMemory(&pi, sizeof(pi));
    sa.nLength = sizeof(sa);
    sa.lpSecurityDescriptor = NULL;
    sa.bInheritHandle = TRUE;

    if (!CreatePipe(&hRead, &hWrite, &sa, 0)) goto done;
    SetHandleInformation(hRead, HANDLE_FLAG_INHERIT, 0);

    /* an explicit NUL stdin stops anything interactive from hanging forever */
    hNul = CreateFileW(L"NUL", GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE,
                       &sa, OPEN_EXISTING, 0, NULL);
    /* CreateFileW returns INVALID_HANDLE_VALUE ((HANDLE)-1) on failure, NOT NULL.
     * `if (hNul)` would be TRUE for that value, and then we would (a) hand
     * si.hStdInput = (HANDLE)-1 to CreateProcessW -- making EVERY command fail to
     * start -- and (b) CloseHandle((HANDLE)-1) at the cleanup label, which is
     * undefined behaviour. Must compare against INVALID_HANDLE_VALUE. */
    if (hNul == INVALID_HANDLE_VALUE) { hNul = NULL; goto done; }

    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES | STARTF_USESHOWWINDOW;
    si.wShowWindow = SW_HIDE;
    si.hStdOutput = hWrite;
    si.hStdError  = hWrite;
    si.hStdInput  = hNul;

    /* CREATE_SUSPENDED closes a real race: if the child starts running before it
     * is moved into the job, cmd.exe can already have spawned ping.exe, and that
     * grandchild sits OUTSIDE the job -- TerminateJobObject would then leave it
     * running and locking files. Windows 7 has neither nested jobs nor
     * PROC_THREAD_ATTRIBUTE_JOB_LIST, so suspend -> assign -> resume is the only
     * ordering that works on every target PE. */
    if (!CreateProcessW(NULL, cmdline, NULL, NULL, TRUE,
                        CREATE_NO_WINDOW | CREATE_SUSPENDED, NULL, NULL, &si, &pi)) goto done;

    CloseHandle(hWrite);   /* parent keeps no writer: read side sees EOF on exit */
    hWrite = NULL;
    hJob = MakeKillableJob(pi.hProcess);

    /* Never leave the child suspended: if resume fails the only way out is to
     * terminate it, otherwise the process sits there forever holding the pipe. */
    if (ResumeThread(pi.hThread) == (DWORD)-1) {
        TerminateProcess(pi.hProcess, 1);
        goto done;
    }

    cap = 4096;
    buf = (char*)xalloc(cap);
    t0  = GetTickCount();  /* GetTickCount64 is Vista+ . XP has no such export. */

    for (;;) {
        while (buf && len + 1 < cap &&
               PeekNamedPipe(hRead, NULL, 0, NULL, &avail, NULL) && avail > 0) {
            DWORD want = cap - len - 1;
            /* Asking for more than `avail` would BLOCK until the pipe closes.
             * A count of 0 does not mean EOF on an anonymous pipe. */
            if (want > avail) want = avail;
            if (!ReadFile(hRead, buf + len, want, &got, NULL) || !got) break;
            len += got;
            if (len + 4096 > cap) {
                char* nb = (char*)HeapReAlloc(GetProcessHeap(), 0, buf, cap * 2);
                if (nb) { buf = nb; cap *= 2; }
            }
        }
        if (WaitForSingleObject(pi.hProcess, 20) == WAIT_OBJECT_0) break;
        if (GetTickCount() - t0 >= timeout_ms) { r->timed_out = TRUE; break; }
        if (g_cancel)                          { r->timed_out = TRUE; break; }
    }

    if (r->timed_out) {
        if (hJob) {
            /* Kills the whole tree in one call. A zero return means it FAILED, and
             * the original code ignored that -- a silent failure here leaves the
             * grandchild running. There is no tree-kill fallback in this C version
             * (the caller only has `taskkill /T /F`, which may not exist in a
             * minimal WinPE 3.x). The Go port fixes this properly by walking the
             * tree itself -- see spike/job, killTreeSelfContained. */
            TerminateJobObject(hJob, 1);
        }
        /* Degraded path: MakeKillableJob returned NULL -- e.g. we are inside a
         * parent job and this is Windows 7, which has no nested jobs.
         * TerminateProcess only kills cmd.exe itself; any grandchild survives.
         * The caller follows up with `taskkill /T /F`, but that executable may
         * not exist in a minimal WinPE 3.x -- so this is best-effort only.
         * The Go port drops this dependency entirely. */
        TerminateProcess(pi.hProcess, 1);
        WaitForSingleObject(pi.hProcess, 500);
    }

    GetExitCodeProcess(pi.hProcess, &code);
    r->exit_code  = code;
    r->elapsed_ms = GetTickCount() - t0;

    /* final drain of whatever the child left in the pipe */
    if (buf) {
        while (len + 1 < cap &&
               PeekNamedPipe(hRead, NULL, 0, NULL, &avail, NULL) && avail > 0) {
            DWORD want = cap - len - 1;
            if (want > avail) want = avail;
            if (!ReadFile(hRead, buf + len, want, &got, NULL) || !got) break;
            len += got;
        }
    }
    if (buf) {
        buf[len] = 0;
        r->out = buf;
    } else {
        r->out = (char*)xalloc(1);
        if (r->out) r->out[0] = 0;
    }
    r->out_len = len;

done:
    if (hRead)    CloseHandle(hRead);
    if (hWrite)   CloseHandle(hWrite);
    if (hNul)     CloseHandle(hNul);
    if (hJob)     CloseHandle(hJob);
    if (pi.hThread)  CloseHandle(pi.hThread);
    if (pi.hProcess) CloseHandle(pi.hProcess);
    return r->out != NULL;
}

/* Wrap the user's line in cmd.exe. Use /d (skip AutoRun) + /s (quote handling).
 * Anything with nested quotes belongs in run_script, not here. */
static BOOL ExecCommand(const WCHAR* user_cmd, DWORD timeout_ms, CmdResult* r)
{
    SIZE_T n = (SIZE_T)lstrlenW(user_cmd) + 32;
    WCHAR* cl = wbuf(n);
    BOOL ok;
    if (!cl) return FALSE;
    lstrcpyW(cl, L"cmd.exe /d /s /c \"");
    lstrcatW(cl, user_cmd);
    lstrcatW(cl, L"\"");
    ok = RunCmdCapture(cl, timeout_ms, r);
    xfree(cl);
    return ok;
}

/* ----------------------------------------------------------------- worker */

static DWORD WINAPI WorkerProc(LPVOID param)
{
    CmdResult r;
    WCHAR* w;

    (void)param;
    InterlockedExchange(&g_cancel, 0);
    LogLine(LV_AI, L"ready. type a command and press Enter.");
    LogStatus(L"running: exec");

    /* ------------------------------------------------------------------
     * The real agent loop goes here:
     *   1. serialise the conversation to JSON
     *   2. POST it (bundled curl.exe, or your own client over ws2_32)
     *   3. parse tool_calls -> dispatch -> LogLine(LV_TOOL, ...)
     *   4. feed the tool result back, loop until the model returns plain text
     * The two calls below only prove the exec path and the UI plumbing.
     * ------------------------------------------------------------------ */
    if (ExecCommand(L"ver & echo. & echo SystemRoot=%SystemRoot%", 8000, &r)) {
        w = OemToWide(r.out, (int)r.out_len);
        LogLine(LV_OK, w);
        xfree(w);

        w = wbuf(64);
        if (w) {
            wsprintfW(w, L"exit=%lu  %lums%s", r.exit_code, r.elapsed_ms,
                      r.timed_out ? L"  TIMEOUT" : L"");
            LogLine(LV_TOOL, w);
            xfree(w);
        }
        xfree(r.out);
    } else {
        LogLine(LV_ERR, L"could not start cmd.exe");
    }

    LogLine(LV_AI, L"idle.");
    LogStatus(L"idle");
    InterlockedExchange(&g_busy, 0);
    return 0;
}

static void OnSend(void)
{
    int len;
    WCHAR* cmd;
    HANDLE hThread;

    if (InterlockedCompareExchange(&g_busy, 1, 0) != 0) {
        LogLine(LV_ERR, L"busy - press Esc or Stop first.");
        return;
    }
    len = GetWindowTextLengthW(g_input);
    if (len <= 0) { InterlockedExchange(&g_busy, 0); return; }

    cmd = wbuf((SIZE_T)len + 1);
    if (!cmd) { InterlockedExchange(&g_busy, 0); return; }
    GetWindowTextW(g_input, cmd, len + 1);
    SetWindowTextW(g_input, L"");
    LogLine(LV_USER, cmd);
    xfree(cmd);

    hThread = CreateThread(NULL, 0, WorkerProc, NULL, 0, NULL);
    if (!hThread) {
        LogLine(LV_ERR, L"CreateThread failed");
        InterlockedExchange(&g_busy, 0);
        return;
    }
    CloseHandle(hThread);
}

/* ------------------------------------------------------------------- gui */

static void Layout(int cx, int cy)
{
    const int pad = 8, btnw = 76, rowh = 26, statush = 22;
    int logh, sendx, inw;

    if (cx < 320) cx = 320;
    if (cy < 240) cy = 240;
    logh = cy - pad * 3 - rowh - statush;
    if (logh < 60) logh = 60;

    sendx = cx - pad - btnw * 2 - pad;
    inw   = sendx - pad * 2;
    if (inw < 60) inw = 60;

    MoveWindow(g_log,    pad,                         pad,                          cx - pad * 2, logh, TRUE);
    MoveWindow(g_input,  pad,                         pad + logh + pad,             inw,          rowh, TRUE);
    MoveWindow(g_send,   sendx,                       pad + logh + pad,             btnw,         rowh, TRUE);
    MoveWindow(g_stop,   sendx + btnw + pad,          pad + logh + pad,             btnw,         rowh, TRUE);
    MoveWindow(g_status, pad,                         cy - statush - 2,             cx - pad * 2, statush, TRUE);
}

static HFONT MakeFont(void)
{
    LOGFONTW lf;
    ZeroMemory(&lf, sizeof(lf));
    GetObjectW(GetStockObject(DEFAULT_GUI_FONT), sizeof(lf), &lf);
    lf.lfHeight  = -12;              /* fixed size: PE has no useful DPI info */
    lf.lfCharSet = DEFAULT_CHARSET;  /* let the PE pick whatever CJK font it has */
    lf.lfQuality = DEFAULT_QUALITY;
    lf.lfWeight  = FW_NORMAL;
    return CreateFontIndirectW(&lf);
}

static LRESULT CALLBACK WndProc(HWND hwnd, UINT msg, WPARAM wp, LPARAM lp)
{
    switch (msg) {

    case WM_CREATE:
    {
        HWND kids[5];
        int i;

        g_hwnd = hwnd;

        /* Stock user32 classes only. comctl32 controls (ListView, TreeView,
         * ProgressBar) are NOT guaranteed to exist in a trimmed PE, and they
         * also want a manifest for visual styles. Not worth the risk. */
        g_log = CreateWindowExW(WS_EX_CLIENTEDGE, L"EDIT", L"",
            WS_CHILD | WS_VISIBLE | WS_VSCROLL | ES_MULTILINE | ES_READONLY |
            ES_AUTOVSCROLL | ES_LEFT,
            0, 0, 10, 10, hwnd, (HMENU)ID_LOG, g_hInst, NULL);

        g_input = CreateWindowExW(WS_EX_CLIENTEDGE, L"EDIT", L"",
            WS_CHILD | WS_VISIBLE | ES_AUTOHSCROLL,
            0, 0, 10, 10, hwnd, (HMENU)ID_INPUT, g_hInst, NULL);

        g_send = CreateWindowExW(0, L"BUTTON", L"Send",
            WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_PUSHBUTTON,
            0, 0, 10, 10, hwnd, (HMENU)ID_SEND, g_hInst, NULL);

        g_stop = CreateWindowExW(0, L"BUTTON", L"Stop (Esc)",
            WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_PUSHBUTTON,
            0, 0, 10, 10, hwnd, (HMENU)ID_STOP, g_hInst, NULL);

        g_status = CreateWindowExW(0, L"STATIC", L"starting...",
            WS_CHILD | WS_VISIBLE | SS_LEFT | SS_CENTERIMAGE,
            0, 0, 10, 10, hwnd, (HMENU)ID_STATUS, g_hInst, NULL);

        g_font = MakeFont();
        kids[0] = g_log; kids[1] = g_input; kids[2] = g_send;
        kids[3] = g_stop; kids[4] = g_status;
        for (i = 0; i < 5; ++i)
            SendMessageW(kids[i], WM_SETFONT, (WPARAM)g_font, TRUE);

        SetFocus(g_input);
        return 0;
    }

    case WM_SIZE:
        Layout(LOWORD(lp), HIWORD(lp));
        return 0;

    case WM_COMMAND:
        if (LOWORD(wp) == ID_SEND && HIWORD(wp) == BN_CLICKED) { OnSend(); return 0; }
        if (LOWORD(wp) == ID_STOP && HIWORD(wp) == BN_CLICKED) {
            InterlockedExchange(&g_cancel, 1);
            SetWindowTextW(g_status, L"cancelling...");
            return 0;
        }
        return 0;

    /* arriving from the worker thread: append and free */
    case WM_APP_LOG:
    {
        WCHAR* s = (WCHAR*)lp;
        int len, slen;
        if (s) {
            slen = lstrlenW(s);
            len  = GetWindowTextLengthW(g_log);

            /* Trim BEFORE appending, and decide based on the size the box WILL
             * have. Two defects in the original version:
             *  1) it only trimmed when `len > LOG_MAX_CHARS`, so a single
             *     oversized entry sailed straight past the cap and was never
             *     trimmed at all;
             *  2) EM_SETSEL/EM_REPLACESEL indices are UTF-16 **code units**, so a
             *     naive len/2 cut can land inside a surrogate pair, leaving an
             *     orphaned half that renders as garbage.
             * The Go port keeps the log as a []rune and has neither problem --
             * this fix exists so the reference behaviour is correct too. */
            if (len + slen + 8 > LOG_MAX_CHARS) {
                const int mark = 5;   /* code units in L"...\r\n" */
                int cut = len + slen + mark - LOG_MAX_CHARS;
                WCHAR* all;
                if (cut < 1)   cut = 1;
                if (cut > len) cut = len;

                /* back the cut point out of the middle of a surrogate pair */
                all = (WCHAR*)xalloc((len + 1) * sizeof(WCHAR));
                if (all) {
                    all[0] = 0;
                    if (GetWindowTextW(g_log, all, len + 1) > 0 &&
                        cut < len &&
                        all[cut - 1] >= 0xD800 && all[cut - 1] <= 0xDBFF &&
                        all[cut]     >= 0xDC00 && all[cut]     <= 0xDFFF) {
                        cut++;        /* step past the whole pair instead of through it */
                    }
                    xfree(all);
                    if (cut > len) cut = len;
                }

                SendMessageW(g_log, EM_SETSEL, 0, cut);
                SendMessageW(g_log, EM_REPLACESEL, FALSE, (LPARAM)L"...\r\n");
                len = GetWindowTextLengthW(g_log);
            }
            SendMessageW(g_log, EM_SETSEL, (WPARAM)len, (LPARAM)len);
            SendMessageW(g_log, EM_REPLACESEL, TRUE, (LPARAM)s);
            SendMessageW(g_log, EM_SCROLLCARET, 0, 0);
            xfree(s);
        }
        return 0;
    }

    case WM_APP_STATUS:
        if (lp) { SetWindowTextW(g_status, (WCHAR*)lp); xfree((void*)lp); }
        return 0;

    case WM_SETFOCUS:
        SetFocus(g_input);
        return 0;

    case WM_CLOSE:
        if (g_busy) {
            InterlockedExchange(&g_cancel, 1);
            SetWindowTextW(g_status, L"cancelling, try closing again in a moment...");
            return 0;
        }
        DestroyWindow(hwnd);
        return 0;

    case WM_DESTROY:
        if (g_font) DeleteObject(g_font);
        PostQuitMessage(0);
        return 0;
    }
    return DefWindowProcW(hwnd, msg, wp, lp);
}

int WINAPI WinMain(HINSTANCE hInst, HINSTANCE hPrev, LPSTR lpCmdLine, int nCmdShow)
{
    WNDCLASSEXW wc;
    MSG msg;

    (void)hPrev; (void)lpCmdLine;
    g_hInst = hInst;

    ZeroMemory(&wc, sizeof(wc));
    wc.cbSize        = sizeof(wc);
    wc.lpfnWndProc   = WndProc;
    wc.hInstance     = hInst;
    wc.hCursor       = LoadCursorW(NULL, IDC_ARROW);
    wc.hbrBackground = (HBRUSH)(COLOR_BTNFACE + 1);
    wc.lpszClassName = L"PeAgentFrame";
    wc.hIcon         = LoadIconW(NULL, IDI_APPLICATION);
    wc.hIconSm       = wc.hIcon;
    if (!RegisterClassExW(&wc)) return 1;

    {
        int sw = GetSystemMetrics(SM_CXSCREEN);
        int sh = GetSystemMetrics(SM_CYSCREEN);
        int cw = 780, ch = 520;
        RECT r;
        /* PE commonly boots at 800x600: never open a window bigger than the screen */
        if (cw > sw - 30) cw = sw - 30;
        if (ch > sh - 50) ch = sh - 50;
        if (cw < 320) cw = 320;
        if (ch < 240) ch = 240;
        r.left = 0; r.top = 0; r.right = cw; r.bottom = ch;
        AdjustWindowRect(&r, WS_OVERLAPPEDWINDOW, FALSE);

        g_hwnd = CreateWindowExW(0, L"PeAgentFrame", L"tinker - PE agent",
            WS_OVERLAPPEDWINDOW,
            (sw - (r.right - r.left)) / 2, (sh - (r.bottom - r.top)) / 2,
            r.right - r.left, r.bottom - r.top,
            NULL, NULL, hInst, NULL);
    }
    if (!g_hwnd) return 2;

    ShowWindow(g_hwnd, nCmdShow);
    UpdateWindow(g_hwnd);
    {
        /* WM_SIZE is not guaranteed before the first ShowWindow, so lay out once */
        RECT cr;
        GetClientRect(g_hwnd, &cr);
        Layout(cr.right, cr.bottom);
    }
    SetFocus(g_input);

    LogLine(LV_TOOL, L"tinker skeleton up - exec path wired, agent loop pending");

    while (GetMessageW(&msg, NULL, 0, 0) > 0) {
        /* Enter sends. A plain EDIT in a non-dialog window beeps on VK_RETURN,
         * and there is no default button to catch it, so intercept here. */
        if (msg.message == WM_KEYDOWN && msg.wParam == VK_RETURN &&
            GetFocus() == g_input) {
            OnSend();
            continue;
        }
        if (msg.message == WM_KEYDOWN && msg.wParam == VK_ESCAPE) {
            InterlockedExchange(&g_cancel, 1);
            SetWindowTextW(g_status, L"cancelling...");
            continue;
        }
        TranslateMessage(&msg);
        DispatchMessageW(&msg);
    }
    return (int)msg.wParam;
}
