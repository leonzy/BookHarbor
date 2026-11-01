/** Recognize both current and historical built-in IDs in the same UI groups. */
export function builtinPluginType(plugin?: { plugin_key?: string }) {
    const [namespace, type = ''] = String(plugin?.plugin_key || '').split('.');
    return ['bookharbor', 'talebook'].includes(namespace) ? type : '';
}
