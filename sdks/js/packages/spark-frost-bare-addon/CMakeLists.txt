cmake_minimum_required(VERSION 4.0)

find_package(cmake-bare REQUIRED PATHS node_modules/cmake-bare)
find_package(cmake-cargo REQUIRED PATHS node_modules/cmake-cargo)

project(bare_addon C)

# PE output participates in byte-for-byte freshness checks. MSVC needs the
# reproducibility flag during both object compilation and final linking.
if (WIN32)
  add_compile_options("/Brepro")
  add_link_options("/Brepro")
endif ()

add_crate()

add_bare_module(bare_addon)

target_sources(
  ${bare_addon}
  PRIVATE
    binding.c
)

target_link_libraries(
  ${bare_addon}
  PRIVATE
    spark_frost_bare_addon
)

# Link required Windows system libraries when building on Windows
if (WIN32)
  target_link_libraries(
    ${bare_addon}
    PRIVATE
      bcrypt
  )
endif ()
