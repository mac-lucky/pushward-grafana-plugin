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
}
