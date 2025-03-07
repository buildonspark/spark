module.exports = function override (config, env) {
    config.resolve.fallback = Object.assign(config.resolve.fallback || {}, {
        stream: require.resolve('stream-browserify'),
    })
    return config
}
