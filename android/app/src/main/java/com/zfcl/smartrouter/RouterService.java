package com.zfcl.smartrouter;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;
import java.io.BufferedReader;
import java.io.File;
import java.io.InputStreamReader;
import java.net.HttpURLConnection;
import java.net.URL;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;

public final class RouterService extends Service {
    public static final String ACTION_START = "com.zfcl.smartrouter.START";
    public static final String ACTION_STOP = "com.zfcl.smartrouter.STOP";
    private static final String CHANNEL = "smart_router_service";
    private static final int NOTIFICATION_ID = 8787;

    private final ExecutorService executor = Executors.newCachedThreadPool();
    private volatile Process process;
    private volatile boolean stopping;
    private PowerManager.WakeLock wakeLock;

    @Override public void onCreate() {
        super.onCreate();
        createChannel();
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        String action = intent == null ? ACTION_START : intent.getAction();
        if (ACTION_STOP.equals(action)) {
            stopRouter();
            return START_NOT_STICKY;
        }
        Notification n = notification("Starting local router…");
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);
        } else {
            startForeground(NOTIFICATION_ID, n);
        }
        executor.execute(this::ensureRouter);
        return START_STICKY;
    }

    private void ensureRouter() {
        if (isHealthy()) {
            updateNotification("Router active · " + RouterPrefs.baseUrl(this) + "/v1");
            acquireWakeLock();
            return;
        }
        try {
            File executable = new File(getApplicationInfo().nativeLibraryDir, "libsmart_router_exec.so");
            if (!executable.canExecute()) executable.setExecutable(true, true);
            ProcessBuilder pb = new ProcessBuilder(executable.getAbsolutePath(), "--no-browser");
            pb.redirectErrorStream(true);
            pb.environment().put("HOME", getFilesDir().getAbsolutePath());
            pb.environment().put("XDG_CONFIG_HOME", RouterPrefs.configRoot(this).getAbsolutePath());
            process = pb.start();
            acquireWakeLock();

            Process owned = process;
            executor.execute(() -> drain(owned));
            for (int i = 0; i < 60 && !stopping; i++) {
                if (isHealthy()) {
                    updateNotification("Router active · " + RouterPrefs.baseUrl(this) + "/v1");
                    break;
                }
                Thread.sleep(100);
            }
            if (owned != null) {
                int code = owned.waitFor();
                if (!stopping && !isHealthy()) {
                    updateNotification("Router stopped · exit " + code);
                    stopSelf();
                }
            }
        } catch (Exception e) {
            updateNotification("Router failed to start");
            stopSelf();
        }
    }

    private void drain(Process p) {
        if (p == null) return;
        try (BufferedReader r = new BufferedReader(new InputStreamReader(p.getInputStream()))) {
            while (r.readLine() != null) { }
        } catch (Exception ignored) { }
    }

    private boolean isHealthy() {
        HttpURLConnection c = null;
        try {
            c = (HttpURLConnection) new URL(RouterPrefs.baseUrl(this) + "/api/state").openConnection();
            c.setConnectTimeout(350);
            c.setReadTimeout(350);
            c.setUseCaches(false);
            return c.getResponseCode() == 200;
        } catch (Exception ignored) {
            return false;
        } finally {
            if (c != null) c.disconnect();
        }
    }

    private void stopRouter() {
        stopping = true;
        executor.execute(() -> {
            HttpURLConnection c = null;
            try {
                c = (HttpURLConnection) new URL(RouterPrefs.baseUrl(this) + "/api/stop").openConnection();
                c.setRequestMethod("POST");
                c.setConnectTimeout(500);
                c.setReadTimeout(500);
                c.setDoOutput(true);
                c.getOutputStream().write(new byte[0]);
                c.getResponseCode();
            } catch (Exception ignored) {
            } finally {
                if (c != null) c.disconnect();
            }
            Process p = process;
            if (p != null) {
                p.destroy();
                try {
                    if (!p.waitFor(600, TimeUnit.MILLISECONDS)) p.destroyForcibly();
                } catch (Exception ignored) { }
            }
            releaseWakeLock();
            stopForeground(true);
            stopSelf();
        });
    }

    private void acquireWakeLock() {
        if (wakeLock != null && wakeLock.isHeld()) return;
        PowerManager pm = (PowerManager) getSystemService(Context.POWER_SERVICE);
        wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "SmartRouter:LocalProxy");
        wakeLock.setReferenceCounted(false);
        wakeLock.acquire();
    }

    private void releaseWakeLock() {
        if (wakeLock != null && wakeLock.isHeld()) wakeLock.release();
    }

    private void createChannel() {
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel channel = new NotificationChannel(CHANNEL, "Smart Router", NotificationManager.IMPORTANCE_LOW);
            channel.setDescription("Keeps the local OpenRouter proxy available for agents.");
            getSystemService(NotificationManager.class).createNotificationChannel(channel);
        }
    }

    private Notification notification(String text) {
        Intent open = new Intent(this, MainActivity.class);
        PendingIntent openPi = PendingIntent.getActivity(this, 1, open, PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        Intent stop = new Intent(this, RouterService.class).setAction(ACTION_STOP);
        PendingIntent stopPi = PendingIntent.getService(this, 2, stop, PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        return new Notification.Builder(this, CHANNEL)
                .setSmallIcon(android.R.drawable.stat_notify_sync_noanim)
                .setContentTitle("Smart Router")
                .setContentText(text)
                .setContentIntent(openPi)
                .setOngoing(true)
                .addAction(new Notification.Action.Builder(android.R.drawable.ic_menu_close_clear_cancel, "Stop", stopPi).build())
                .build();
    }

    private void updateNotification(String text) {
        getSystemService(NotificationManager.class).notify(NOTIFICATION_ID, notification(text));
    }

    @Override public void onDestroy() {
        releaseWakeLock();
        executor.shutdownNow();
        super.onDestroy();
    }

    @Override public IBinder onBind(Intent intent) { return null; }
}
