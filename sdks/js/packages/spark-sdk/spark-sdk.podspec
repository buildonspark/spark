require "json"

package = JSON.parse(File.read(File.join(__dir__, "package.json"))) # Adjusted path to package.json assuming podspec is in ios/

Pod::Spec.new do |s|
  s.name         = "spark-sdk" # This should match the name in package.json if it's the library name
  s.version      = package["version"]
  s.summary      = package["description"]
  s.homepage     = package["homepage"]
  s.license      = package["license"]
  s.authors      = package["author"]
  s.platforms    = { :ios => "12.0" }
  
  # This path is relative to the podspec file. 
  # Since react-native.config.js uses dist/spark-sdk.podspec,
  # and tsup copies sources to dist/ios/, this path becomes "ios"
  s.source       = { :path => "." } 
  
  # Source files are located in the "ios" subdirectory relative to the podspec location in dist/
  s.source_files = "ios/*.{h,m,mm,swift}"
  s.dependency "React"
  s.dependency "React-Core" # Or "React-CoreModules" depending on RN version and setup
  s.vendored_frameworks = "ios/spark_frostFFI.xcframework"

  s.swift_version = "5.0"
  
  s.pod_target_xcconfig = {
    'DEFINES_MODULE' => 'YES'
  }
  
  # This should be the name of your Swift module if it's different from the pod name.
  # Often, it's the name of the target in Xcode.
  s.module_name = "SparkFrostModule" 
end