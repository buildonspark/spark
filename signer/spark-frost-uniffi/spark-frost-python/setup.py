from setuptools import setup

setup(
    name="spark_frost_python",
    version="0.0.7",
    description="The Python language bindings for spark frost signer",
    long_description=open("README.md").read(),
    long_description_content_type="text/markdown",
    include_package_data = True,
    zip_safe=False,
    packages=["spark_frost"],
    package_dir={"spark_frost": "./src/spark_frost"},
    url="https://github.com/lightsparkdev/spark-go",
    author="Lightspark Group, Inc. <info@lightspark.com>",
    license="Apache 2.0",
    has_ext_modules=lambda: True,
)
