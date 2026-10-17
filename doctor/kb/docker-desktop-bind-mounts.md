<!-- SPDX-License-Identifier: CC-BY-SA-4.0 -->
<!-- Adapted from netresearch/docker-development-skill (docker-via-wsl, bind-mount-ownership), (c) Netresearch DTT GmbH -->

# Bind mounts with Docker Desktop on Windows

This stack bind-mounts folders from the repo into containers (`./rl_agent`
into `ros-server`, `sim-controller` and `dashboard`; `./dashboard`;
`./docker/fly_brain`; `./.git`) and the `mongodb`, `saved_models` and
`tfrecords` folders next to the repo. Docker
Desktop's engine runs inside a WSL 2 VM, so every Windows path in a bind mount
is translated to a VM path. Most bind-mount failures come from that
translation or from Linux file ownership.

What works: the install folder on a local NTFS drive (`C:\...`), with `docker`
and `docker compose` run from PowerShell in that folder. That is how the
installer runs.

## Bind source missing, so Docker created an empty folder

**Symptoms**
- `npm error enoent Could not read package.json` / `ENOENT: no such file or directory, open '/dashboard/package.json'`
- `python: can't open file 'robotaxi.py': [Errno 2] No such file or directory`
- `could not read from input file: Is a directory`
- `not a directory: unknown: Are you trying to mount a directory onto a file (or vice-versa)?`
- An empty folder appears on the host where a file or the repo used to be.

**Cause** When a bind source does not exist, Docker creates it as an empty
directory and mounts that. It happens when the install folder was moved,
renamed or deleted while containers still existed (Docker restarts them with
the old absolute paths), when a single file that is bind-mounted is missing, or
when the path was translated wrongly (next section).

**Check** (doctor can run)
- `docker inspect <container> --format '{{range .Mounts}}{{.Destination}} <- {{.Source}}{{println}}{{end}}'`:
  compare each `Source` with the actual install folder.
- `docker compose ps -a` from the install folder: a container whose project
  folder (`com.docker.compose.project.working_dir` label in `docker inspect`)
  is not the current folder belongs to an old copy.
- `Test-Path <expected file>` and `Get-ChildItem <expected folder>` on the host.

**Fix** From the current install folder: `docker compose up -d --force-recreate`.
Then delete any empty folders Docker created at the old paths. If the host
copy itself is missing files, restore them with `git status` / `git checkout -- <path>`.

## Install folder on a network drive, UNC path or WSL share

**Symptoms**
- A path segment appears twice in the mount source, e.g. `/run/desktop/mnt/host/uC/.../user/user/proj`.
- The container sees an empty folder or a phantom directory while the host
  shows the files.
- Edits on the host do not appear in the container.

**Cause** Docker Desktop translates bind paths from mapped network drives
(`Z:`), UNC paths (`\\server\share`) and `\\wsl$\...` paths unreliably. Folders
synced by OneDrive with Files On-Demand can also hold cloud-only placeholders
that fail to read inside a container.

**Check** (doctor can run)
- `Get-PSDrive <letter>` (a `DisplayRoot` of `\\...` means a network drive)
- `(Get-Location).Path` and the mount sources from `docker inspect` above.
- `Get-Item <install folder> | Select-Object FullName, Attributes` (an
  `Offline` or `ReparsePoint` attribute suggests a cloud-only OneDrive file).

**Fix** Move the install to a local folder (e.g. `C:\rl-roboracer`) and
reinstall there, or run every `docker` command from inside WSL against a
native Linux path: `wsl.exe -e bash -lc "cd /home/<user>/rl-roboracer && docker compose up -d"`.
Do not mix the two: a project started from WSL paths must be managed from WSL.

## Container user cannot write to a bind-mounted folder

**Symptoms**
- `mkdir: cannot create directory '/bitnami/mongodb/data': Permission denied`
- `EACCES: permission denied` writing into a mounted folder.
- A service that runs as a non-root user fails on its first write; services
  running as root are fine.

**Cause** Folders Docker creates itself for a bind mount on a Windows drive
come out owned by `root` and are read-only to other Linux users. MongoDB runs
as user 1001.

**Check** `docker compose logs --tail 50 mongo`; `Get-Acl <folder>`.

**Fix** Give the folder to the container's user with a throwaway container:
`docker compose run --rm --no-deps --user root --entrypoint chown mongo -R 1001:1001 /bitnami/mongodb`.

## Root-owned files when the repo lives in the WSL filesystem

**Symptoms** (repo under `/home/<user>/...` inside a WSL distro)
- `npm error EACCES: permission denied, rename '.../node_modules/...'`
- `rm: cannot remove '...': Permission denied`, `git clean` failures.
- The container run succeeded; the next host-side command fails.

**Cause** A container running as root wrote into the bind-mounted folder, so
the files are root-owned on the Linux side and the WSL user cannot change them.
(On a Windows drive this does not happen; Windows ACLs apply instead.)

**Check** `find node_modules -maxdepth 2 -user root | head` inside the distro.

**Fix** Hand the files back without sudo:
`docker run --rm -v "$PWD:/work" -w /work alpine chown -R "$(id -u):$(id -g)" /work`,
or delete disposable output (`node_modules`, build folders) the same way and
rebuild as the host user.
