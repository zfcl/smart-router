package com.zfcl.smartrouter;

import android.Manifest;
import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.Gravity;
import android.view.View;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.LinearLayout;
import android.widget.TextView;
import java.net.HttpURLConnection;
import java.net.URL;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public final class MainActivity extends Activity {
    private final Handler main = new Handler(Looper.getMainLooper());
    private final ExecutorService io = Executors.newSingleThreadExecutor();
    private WebView web;
    private TextView status;
    private int attempts;

    @Override protected void onCreate(Bundle state) {
        super.onCreate(state);
        buildUi();
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 17);
        }
        Intent service = new Intent(this, RouterService.class).setAction(RouterService.ACTION_START);
        startForegroundService(service);
        waitForRouter();
    }

    private void buildUi() {
        FrameLayout root = new FrameLayout(this);
        root.setBackgroundColor(Color.rgb(13, 17, 23));

        web = new WebView(this);
        web.setBackgroundColor(Color.rgb(13, 17, 23));
        WebSettings settings = web.getSettings();
        settings.setJavaScriptEnabled(true);
        settings.setDomStorageEnabled(true);
        settings.setAllowFileAccess(false);
        settings.setAllowContentAccess(false);
        settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        settings.setTextZoom(100);
        web.setWebViewClient(new WebViewClient() {
            @Override public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                String host = request.getUrl().getHost();
                return !("127.0.0.1".equals(host) || "localhost".equals(host));
            }
        });
        root.addView(web, new FrameLayout.LayoutParams(FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT));

        LinearLayout overlay = new LinearLayout(this);
        overlay.setOrientation(LinearLayout.VERTICAL);
        overlay.setGravity(Gravity.CENTER);
        overlay.setPadding(dp(32), dp(32), dp(32), dp(32));
        overlay.setTag("overlay");

        TextView mark = new TextView(this);
        mark.setText("SR");
        mark.setTextColor(Color.rgb(232, 237, 243));
        mark.setTextSize(42);
        mark.setGravity(Gravity.CENTER);
        overlay.addView(mark, new LinearLayout.LayoutParams(dp(120), dp(72)));

        status = new TextView(this);
        status.setText("Starting local router");
        status.setTextColor(Color.rgb(141, 152, 166));
        status.setTextSize(15);
        status.setGravity(Gravity.CENTER);
        LinearLayout.LayoutParams statusLp = new LinearLayout.LayoutParams(LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        statusLp.topMargin = dp(12);
        overlay.addView(status, statusLp);

        root.addView(overlay, new FrameLayout.LayoutParams(FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT));
        setContentView(root);
    }

    private void waitForRouter() {
        attempts = 0;
        poll();
    }

    private void poll() {
        io.execute(() -> {
            boolean ok = health();
            main.post(() -> {
                if (isFinishing()) return;
                if (ok) {
                    View overlay = ((FrameLayout) web.getParent()).findViewWithTag("overlay");
                    if (overlay != null) overlay.setVisibility(View.GONE);
                    web.loadUrl(RouterPrefs.baseUrl(this) + "/");
                } else if (++attempts < 80) {
                    status.setText("Starting local router · " + attempts);
                    main.postDelayed(this::poll, 125);
                } else {
                    status.setText("Router did not start. Reopen the app or check the notification.");
                }
            });
        });
    }

    private boolean health() {
        HttpURLConnection c = null;
        try {
            c = (HttpURLConnection) new URL(RouterPrefs.baseUrl(this) + "/api/state").openConnection();
            c.setConnectTimeout(300);
            c.setReadTimeout(300);
            return c.getResponseCode() == 200;
        } catch (Exception ignored) {
            return false;
        } finally {
            if (c != null) c.disconnect();
        }
    }

    private int dp(int x) {
        return Math.round(x * getResources().getDisplayMetrics().density);
    }

    @Override protected void onDestroy() {
        if (web != null) web.destroy();
        io.shutdownNow();
        super.onDestroy();
    }
}
