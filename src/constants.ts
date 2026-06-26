import pluginJson from './plugin.json';

export const PLUGIN_ID = pluginJson.id;

// Base URL used to build links to this app's pages (e.g. /a/pushward-alerts-app/overview).
export const PLUGIN_BASE_URL = `/a/${PLUGIN_ID}`;

// Base path for the Go backend's custom resource routes. The plugin SDK strips this
// prefix before the request reaches the resource mux, so handlers see "/config" etc.
export const RESOURCE_BASE_URL = `/api/plugins/${PLUGIN_ID}/resources`;

export enum ROUTES {
  Overview = 'overview',
  Connect = 'connect',
  Activities = 'activities',
  Widgets = 'widgets',
}

// Hrefs for cross-page navigation. CONFIG_HREF is Grafana's app-config page
// (/plugins/<id>), the others are this app's own routes under PLUGIN_BASE_URL.
export const CONFIG_HREF = `/plugins/${PLUGIN_ID}`;
export const CONNECT_HREF = `${PLUGIN_BASE_URL}/${ROUTES.Connect}`;
export const ACTIVITIES_HREF = `${PLUGIN_BASE_URL}/${ROUTES.Activities}`;
