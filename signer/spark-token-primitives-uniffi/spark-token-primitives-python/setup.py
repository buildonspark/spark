from setuptools import setup

setup(
    name="spark_token_primitives_python",
    version="0.0.1",
    description="Python bindings for local Spark token transaction helpers",
    long_description=open("README.md").read(),
    long_description_content_type="text/markdown",
    include_package_data=True,
    zip_safe=False,
    packages=["spark_token_primitives"],
    package_dir={"spark_token_primitives": "./src/spark_token_primitives"},
    url="https://github.com/lightsparkdev/spark",
    author="Lightspark Group, Inc. <info@lightspark.com>",
    license="Apache 2.0",
    has_ext_modules=lambda: True,
)
