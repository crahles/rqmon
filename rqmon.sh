#!/bin/sh

### BEGIN INIT INFO
# Provides:           rqmon
# Required-Start:     $syslog $remote_fs
# Required-Stop:      $syslog $remote_fs
# Default-Start:      2 3 4 5
# Default-Stop:       0 1 6
# Short-Description:  Monitoring & Alerting for Resque Queue System.
# Description:
#  Monitoring & Alerting for Resque Queue System.
### END INIT INFO

BASE=$(basename $0)

BIN=/usr/bin/$BASE
PIDFILE=/var/run/$BASE.pid
OPTS=""

PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin

# Get lsb functions
. /lib/lsb/init-functions

if [ -f /etc/default/$BASE ]; then
    . /etc/default/$BASE
fi

# see also init_is_upstart in /lib/lsb/init-functions (which isn't available in Ubuntu 12.04, or we'd use it)
if [ -x /sbin/initctl ] && /sbin/initctl version 2>/dev/null | /bin/grep -q upstart; then
    log_failure_msg "RQMon is managed via upstart, try using service $BASE $1"
    exit 1
fi

# Check rqmon is present
if [ ! -x $BIN ]; then
    log_failure_msg "$BIN not present or not executable"
    exit 1
fi

case "$1" in
    start)
        log_begin_msg "Starting RQMon: $BASE"
        start-stop-daemon --start --background \
            --exec "$BIN" \
            --pidfile "$PIDFILE" \
            -- -d -p "$PIDFILE" \
            $OPTS
        log_end_msg $?
        ;;

    stop)
        log_begin_msg "Stopping RQMon: $BASE"
        start-stop-daemon --stop \
            --pidfile "$PIDFILE"
        log_end_msg $?
        ;;

    restart)
        pid=`cat "$PIDFILE" 2>/dev/null`
        [ -n "$pid" ] \
            && ps -p $pid > /dev/null 2>&1 \
            && $0 stop
        $0 start
        ;;

    force-reload)
        $0 restart
        ;;

    status)
        status_of_proc -p "$PIDFILE" "$BIN" rqmon
        ;;

    *)
        echo "Usage: $0 {start|stop|restart|status}"
        exit 1
        ;;
esac

exit 0
